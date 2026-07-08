package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type screen int

const (
	screenDashboard screen = iota
	screenJobs
	screenArchived
	screenRun
)

var screenNames = []string{"Dashboard", "Jobs", "Archived", "Run"}
var spinnerFrames = []string{"|", "/", "-", "\\"}
var jobReferencePattern = regexp.MustCompile(`(?i)JR-\d+`)

var (
	titleStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	activeTabStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("24")).Padding(0, 1)
	inactiveTabStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Padding(0, 1)
	dividerStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	sectionStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	tableHeadStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("238"))
	selectedRowStyle = lipgloss.NewStyle().Background(lipgloss.Color("252"))
	dimStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	mutedStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	successStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errorStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	infoStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
	scoreHighStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	scoreMidStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	scoreLowStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

type tableCell struct {
	text  string
	style lipgloss.Style
	right bool
}

type model struct {
	appDir           string
	width            int
	height           int
	screen           screen
	scroll           int
	selectedJob      int
	selectedArchived int
	detailOpen       bool
	detailJobID      string
	detailScroll     int
	confirmArchive   bool

	env      envSummary
	jobs     []jobRow
	archived []jobRow
	state    stateSummary
	report   []string
	loaded   time.Time
	loadErr  string

	run       *runState
	runEvents chan tea.Msg
	cancelRun context.CancelFunc
	status    string
}

type envSummary struct {
	resumeFile              string
	model                   string
	baseURL                 string
	jobURLCount             int
	maxJobsPerSource        string
	workdayPageSize         string
	maxNewEvaluationsPerRun string
	hasAPIKey               bool
}

type jobRow struct {
	id            string
	title         string
	company       string
	location      string
	status        string
	score         int
	apply         string
	reference     string
	url           string
	lastSeen      string
	closedAt      string
	expiresAt     string
	archivedAt    string
	archiveReason string
	canApply      string
	analysisPath  string
	analysis      jobAnalysis
	analysisErr   string
}

type stateSummary struct {
	total     int
	active    int
	archived  int
	scored    int
	apply     int
	deferred  int
	cached    int
	evaluated int
	errors    int
	closed    int
}

type jobAnalysis struct {
	JobTitle                string   `json:"job_title"`
	Company                 string   `json:"company"`
	FitScore                int      `json:"fit_score"`
	ShouldApply             bool     `json:"should_apply"`
	Summary                 string   `json:"summary"`
	MatchedSkills           []string `json:"matched_skills"`
	MissingSkills           []string `json:"missing_skills"`
	ExperienceAlignment     string   `json:"experience_alignment"`
	Concerns                []string `json:"concerns"`
	RecommendedResumeTweaks []string `json:"recommended_resume_tweaks"`
}

type analysisFile struct {
	Analysis jobAnalysis `json:"analysis"`
}

type runState struct {
	running      bool
	dryRun       bool
	startedAt    time.Time
	finishedAt   time.Time
	spinnerIndex int
	logs         []string
	summary      map[string]int
	exitErr      string
}

type refreshMsg struct {
	env      envSummary
	jobs     []jobRow
	archived []jobRow
	state    stateSummary
	report   []string
	err      string
	loaded   time.Time
}

type runStartedMsg struct {
	dryRun bool
}

type runEventMsg struct {
	event map[string]any
	raw   string
}

type runDoneMsg struct {
	err string
}

type archiveDoneMsg struct {
	jobID    string
	archived bool
	err      string
}

type tickMsg time.Time

func main() {
	appDir, err := findAppDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "job_goblin tui: %v\n", err)
		os.Exit(1)
	}

	program := tea.NewProgram(initialModel(appDir), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "job_goblin tui: %v\n", err)
		os.Exit(1)
	}
}

func initialModel(appDir string) model {
	return model{
		appDir: appDir,
		screen: screenDashboard,
		status: "Ready",
	}
}

func (m model) Init() tea.Cmd {
	return loadDataCmd(m.appDir)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case refreshMsg:
		m.env = msg.env
		m.jobs = msg.jobs
		m.archived = msg.archived
		m.state = msg.state
		m.report = msg.report
		m.loaded = msg.loaded
		m.loadErr = msg.err
		m.selectedJob = clampSelection(m.selectedJob, len(m.jobs))
		m.selectedArchived = clampSelection(m.selectedArchived, len(m.archived))
		if m.status == "" || strings.HasPrefix(m.status, "Loaded") || m.status == "Ready" {
			m.status = "Loaded local state"
		}
		return m, nil

	case runStartedMsg:
		if msg.dryRun {
			m.status = "Dry run started"
		} else {
			m.status = "Run started"
		}
		return m, tickCmd()

	case runEventMsg:
		if m.run != nil {
			m.run.logs = appendLog(m.run.logs, eventLine(msg))
			if typeName, _ := msg.event["type"].(string); typeName == "summary" {
				m.run.summary = eventSummary(msg.event)
			}
		}
		return m, waitForRunEvent(m.runEvents)

	case runDoneMsg:
		if m.run != nil {
			m.run.running = false
			m.run.finishedAt = time.Now()
			m.run.exitErr = msg.err
		}
		m.cancelRun = nil
		m.runEvents = nil
		if msg.err != "" {
			m.status = "Run failed"
		} else {
			m.status = "Run complete"
		}
		return m, loadDataCmd(m.appDir)

	case archiveDoneMsg:
		m.confirmArchive = false
		if msg.err != "" {
			m.status = msg.err
			return m, nil
		}
		if msg.archived {
			m.status = "Archived job"
		} else {
			m.status = "Unarchived job"
		}
		m.detailOpen = false
		m.detailJobID = ""
		m.detailScroll = 0
		return m, loadDataCmd(m.appDir)

	case tickMsg:
		if m.run != nil && m.run.running {
			m.run.spinnerIndex = (m.run.spinnerIndex + 1) % len(spinnerFrames)
			return m, tickCmd()
		}
		return m, nil

	case tea.KeyMsg:
		return updateKey(m, msg)
	}

	return m, nil
}

func updateKey(m model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	keyName := key.String()
	switch keyName {
	case "ctrl+c", "q":
		if m.cancelRun != nil {
			m.cancelRun()
		}
		return m, tea.Quit
	}

	if m.confirmArchive {
		return updateArchiveConfirmation(m, key)
	}
	if m.detailOpen {
		return updateDetailKey(m, key)
	}

	switch keyName {
	case "tab":
		m.screen = screen((int(m.screen) + 1) % len(screenNames))
		m.scroll = 0
		return m, nil
	case "shift+tab":
		m.screen = screen((int(m.screen) + len(screenNames) - 1) % len(screenNames))
		m.scroll = 0
		return m, nil
	case "1":
		m.screen = screenDashboard
		m.scroll = 0
		return m, nil
	case "2":
		m.screen = screenJobs
		m.scroll = 0
		return m, nil
	case "3":
		m.screen = screenArchived
		m.scroll = 0
		return m, nil
	case "4":
		m.screen = screenRun
		m.scroll = 0
		return m, nil
	case "R":
		m.status = "Refreshing local state"
		return m, loadDataCmd(m.appDir)
	case "r":
		if m.run != nil && m.run.running {
			m.status = "A run is already in progress"
			return m, nil
		}
		return startRun(m, false)
	case "d":
		if m.run != nil && m.run.running {
			m.status = "A run is already in progress"
			return m, nil
		}
		return startRun(m, true)
	case "esc", "c":
		if m.cancelRun != nil {
			m.cancelRun()
			m.status = "Cancelling run"
		}
		return m, nil
	case "enter":
		if job, ok := m.selectedDetailJob(); ok {
			m.detailOpen = true
			m.detailJobID = job.id
			m.detailScroll = 0
			m.status = "Viewing job report"
		}
		return m, nil
	case "a":
		if m.screen == screenJobs {
			if _, ok := m.selectedActiveJob(); !ok {
				m.status = "No job selected"
				return m, nil
			}
			m.confirmArchive = true
			m.status = "Archive selected job? y confirm, n cancel"
		}
		return m, nil
	case "u":
		if m.screen == screenArchived {
			job, ok := m.selectedArchivedJob()
			if !ok {
				m.status = "No archived job selected"
				return m, nil
			}
			m.status = "Unarchiving job"
			return m, setJobArchiveCmd(m.appDir, job.id, false, "")
		}
		return m, nil
	case "up", "k":
		m.moveUp()
		return m, nil
	case "down", "j":
		m.moveDown()
		return m, nil
	case "pgup":
		m.pageUp()
		return m, nil
	case "pgdown":
		m.pageDown()
		return m, nil
	case "home", "g":
		m.scroll = 0
		m.selectedJob = 0
		m.selectedArchived = 0
		return m, nil
	case "end", "G":
		m.scroll = 1_000_000
		if len(m.jobs) > 0 {
			m.selectedJob = len(m.jobs) - 1
		}
		if len(m.archived) > 0 {
			m.selectedArchived = len(m.archived) - 1
		}
		return m, nil
	}

	return m, nil
}

func updateArchiveConfirmation(m model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "y", "Y", "enter":
		job, ok := m.selectedActiveJob()
		if !ok {
			m.confirmArchive = false
			m.status = "No job selected"
			return m, nil
		}
		m.status = "Archiving job"
		return m, setJobArchiveCmd(m.appDir, job.id, true, "manual")
	case "n", "N", "esc", "c":
		m.confirmArchive = false
		m.status = "Archive cancelled"
		return m, nil
	default:
		return m, nil
	}
}

func updateDetailKey(m model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.detailOpen = false
		m.detailJobID = ""
		m.detailScroll = 0
		m.status = "Loaded local state"
	case "up", "k":
		if m.detailScroll > 0 {
			m.detailScroll--
		}
	case "down", "j":
		m.detailScroll++
	case "pgup":
		m.detailScroll = max(0, m.detailScroll-max(1, m.contentHeight()-4))
	case "pgdown":
		m.detailScroll += max(1, m.contentHeight()-4)
	case "home", "g":
		m.detailScroll = 0
	case "end", "G":
		m.detailScroll = 1_000_000
	}
	return m, nil
}

func (m model) selectedActiveJob() (jobRow, bool) {
	if m.selectedJob < 0 || m.selectedJob >= len(m.jobs) {
		return jobRow{}, false
	}
	return m.jobs[m.selectedJob], true
}

func (m model) selectedArchivedJob() (jobRow, bool) {
	if m.selectedArchived < 0 || m.selectedArchived >= len(m.archived) {
		return jobRow{}, false
	}
	return m.archived[m.selectedArchived], true
}

func (m model) selectedDetailJob() (jobRow, bool) {
	switch m.screen {
	case screenJobs:
		return m.selectedActiveJob()
	case screenArchived:
		return m.selectedArchivedJob()
	default:
		return jobRow{}, false
	}
}

func (m model) detailJob() (jobRow, bool) {
	for _, job := range m.jobs {
		if job.id == m.detailJobID {
			return job, true
		}
	}
	for _, job := range m.archived {
		if job.id == m.detailJobID {
			return job, true
		}
	}
	return jobRow{}, false
}

func clampSelection(selected int, length int) int {
	if length <= 0 {
		return 0
	}
	if selected < 0 {
		return 0
	}
	if selected >= length {
		return length - 1
	}
	return selected
}

func startRun(m model, dryRun bool) (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan tea.Msg, 256)
	m.cancelRun = cancel
	m.runEvents = ch
	m.run = &runState{
		running:   true,
		dryRun:    dryRun,
		startedAt: time.Now(),
		summary:   map[string]int{},
	}
	m.screen = screenRun
	m.scroll = 0
	return m, tea.Batch(startRunCmd(ctx, m.appDir, dryRun, ch), waitForRunEvent(ch), tickCmd())
}

func (m *model) moveUp() {
	switch m.screen {
	case screenJobs:
		if m.selectedJob > 0 {
			m.selectedJob--
		}
	case screenArchived:
		if m.selectedArchived > 0 {
			m.selectedArchived--
		}
	case screenRun:
		if m.scroll > 0 {
			m.scroll--
		}
	}
}

func (m *model) moveDown() {
	switch m.screen {
	case screenJobs:
		if m.selectedJob < len(m.jobs)-1 {
			m.selectedJob++
		}
	case screenArchived:
		if m.selectedArchived < len(m.archived)-1 {
			m.selectedArchived++
		}
	case screenRun:
		m.scroll++
	}
}

func (m *model) pageUp() {
	step := max(1, m.contentHeight()-2)
	switch m.screen {
	case screenJobs:
		m.selectedJob = max(0, m.selectedJob-step)
	case screenArchived:
		m.selectedArchived = max(0, m.selectedArchived-step)
	case screenRun:
		m.scroll = max(0, m.scroll-step)
	}
}

func (m *model) pageDown() {
	step := max(1, m.contentHeight()-2)
	switch m.screen {
	case screenJobs:
		m.selectedJob = min(max(0, len(m.jobs)-1), m.selectedJob+step)
	case screenArchived:
		m.selectedArchived = min(max(0, len(m.archived)-1), m.selectedArchived+step)
	case screenRun:
		m.scroll += step
	}
}

func (m model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	var body string
	switch m.screen {
	case screenDashboard:
		body = m.dashboardView()
	case screenJobs:
		body = m.jobsView()
	case screenArchived:
		body = m.archivedView()
	case screenRun:
		body = m.runView()
	default:
		body = m.dashboardView()
	}

	parts := []string{
		m.headerView(),
		body,
		m.footerView(),
	}
	return strings.Join(parts, "\n")
}

func (m model) headerView() string {
	var tabs []string
	for i, name := range screenNames {
		label := fmt.Sprintf("%d %s", i+1, name)
		if screen(i) == m.screen {
			label = activeTabStyle.Render(label)
		} else {
			label = inactiveTabStyle.Render(label)
		}
		tabs = append(tabs, label)
	}

	title := "Job Goblin"
	if m.run != nil && m.run.running {
		title += " " + spinnerFrames[m.run.spinnerIndex]
	}

	return fmt.Sprintf(
		"%s\n%s\n%s",
		titleStyle.Render(title),
		strings.Join(tabs, " "),
		dividerStyle.Render(strings.Repeat("─", m.width)),
	)
}

func (m model) footerView() string {
	help := "r run  d dry-run  R refresh  tab switch  arrows scroll  c cancel  q quit"
	if m.run == nil || !m.run.running {
		help = "r run  d dry-run  R refresh  tab switch  arrows scroll  q quit"
	}
	if m.confirmArchive {
		help = "y archive  n/esc cancel  q quit"
	} else if m.detailOpen {
		help = "esc close  arrows scroll  q quit"
	} else {
		switch m.screen {
		case screenJobs:
			help = "enter details  a archive  arrows move  r run  R refresh  q quit"
		case screenArchived:
			help = "enter details  u unarchive  arrows move  r run  R refresh  q quit"
		}
	}
	status := m.status
	if m.loadErr != "" {
		status = m.loadErr
	}
	return fmt.Sprintf(
		"%s\n%s",
		dividerStyle.Render(strings.Repeat("─", m.width)),
		truncateStyled(statusStyle(status).Render(status)+" "+dimStyle.Render("|")+" "+mutedStyle.Render(help), m.width),
	)
}

func (m model) dashboardView() string {
	lines := []string{
		sectionStyle.Render("Configuration"),
		keyValue("App dir", m.appDir),
		keyValue("Resume", valueOrDash(m.env.resumeFile)),
		keyValue("Model", valueOrDash(m.env.model)),
		keyValue("Base URL", valueOrDash(m.env.baseURL)),
		keyValue("API key configured", boolText(m.env.hasAPIKey)),
		keyValue("Job URL sources", strconv.Itoa(m.env.jobURLCount)),
		keyValue("Max jobs/source", valueOrDash(m.env.maxJobsPerSource)),
		keyValue("Workday page size", valueOrDash(m.env.workdayPageSize)),
		keyValue("Max new evaluations/run", valueOrDash(m.env.maxNewEvaluationsPerRun)),
		"",
		sectionStyle.Render("State"),
		keyValue("Jobs tracked", strconv.Itoa(m.state.total)),
		keyValue("Active jobs", strconv.Itoa(m.state.active)),
		keyValue("Archived jobs", mutedStyle.Render(strconv.Itoa(m.state.archived))),
		keyValue("Scored jobs", strconv.Itoa(m.state.scored)),
		keyValue("Recommended apply", successStyle.Render(strconv.Itoa(m.state.apply))),
		keyValue("Deferred", warnStyle.Render(strconv.Itoa(m.state.deferred))),
		keyValue("Cached", infoStyle.Render(strconv.Itoa(m.state.cached))),
		keyValue("Evaluated", successStyle.Render(strconv.Itoa(m.state.evaluated))),
		keyValue("Errors", errorStyle.Render(strconv.Itoa(m.state.errors))),
		keyValue("Closed/non-applyable", mutedStyle.Render(strconv.Itoa(m.state.closed))),
		"",
		sectionStyle.Render("Files"),
		keyValue("Report file lines", strconv.Itoa(len(m.report))),
		keyValue("Loaded", formatTime(m.loaded)),
	}

	if m.loadErr != "" {
		lines = append(lines, "", errorStyle.Render("Load Error"), errorStyle.Render(m.loadErr))
	}

	return fitLines(lines, m.contentHeight(), m.width)
}

func (m model) jobsView() string {
	if m.detailOpen {
		return m.jobDetailView()
	}
	if len(m.jobs) == 0 {
		return fitLines([]string{"No active jobs are tracked."}, m.contentHeight(), m.width)
	}

	height := m.contentHeight()
	if m.selectedJob < 0 {
		m.selectedJob = 0
	}
	if m.selectedJob >= len(m.jobs) {
		m.selectedJob = len(m.jobs) - 1
	}

	start := m.scroll
	if m.selectedJob < start {
		start = m.selectedJob
	}
	if m.selectedJob >= start+height-3 {
		start = max(0, m.selectedJob-height+4)
	}
	m.scroll = start

	titleW, companyW, locationW := jobColumnWidths(m.width)
	refW := 11
	statusW := 12
	widths := []int{5, statusW, 6, refW, titleW, companyW, locationW}
	lines := []string{
		sectionStyle.Render(fmt.Sprintf("Jobs (%d active)", len(m.jobs))),
		renderTableRow(
			widths,
			[]tableCell{
				{text: "Fit", style: tableHeadStyle, right: true},
				{text: "Status", style: tableHeadStyle},
				{text: "Apply", style: tableHeadStyle},
				{text: "Ref", style: tableHeadStyle},
				{text: "Title", style: tableHeadStyle},
				{text: "Company", style: tableHeadStyle},
				{text: "Location", style: tableHeadStyle},
			},
			tableHeadStyle,
		),
		dividerStyle.Render(strings.Repeat("─", m.width)),
	}

	end := min(len(m.jobs), start+height-len(lines))
	for i := start; i < end; i++ {
		job := m.jobs[i]
		score := "-"
		if job.score >= 0 {
			score = strconv.Itoa(job.score)
		}
		cells := []tableCell{
			{text: score, style: scoreStyle(job.score), right: true},
			{text: job.status, style: jobStatusStyle(job.status)},
			{text: applyText(job), style: applyStyle(job.apply)},
			{text: jobReference(job), style: mutedStyle},
			{text: job.title},
			{text: job.company, style: mutedStyle},
			{text: styledLocation(job.location)},
		}
		if i == m.selectedJob {
			cells = highlightCells(cells)
		}
		line := renderTableRow(widths, cells, lipgloss.NewStyle())
		if i == m.selectedJob {
			line = selectedRowStyle.Render(padStyledLine(line, m.width))
		}
		lines = append(lines, truncateStyled(line, m.width))
	}

	if m.selectedJob >= 0 && m.selectedJob < len(m.jobs) {
		job := m.jobs[m.selectedJob]
		lines = append(
			lines,
			"",
			keyValue("Action", "enter details, a archive"),
			keyValue("Selected ref", jobReference(job)),
			keyValue("Selected locations", valueOrDash(job.location)),
			keyValue("Selected URL", shortenURL(job.url)),
		)
		if m.confirmArchive {
			lines = append(lines, warnStyle.Render("Confirm archive: y archives this job, n or esc cancels."))
		}
	}

	return fitLines(lines, height, m.width)
}

func (m model) archivedView() string {
	if m.detailOpen {
		return m.jobDetailView()
	}
	if len(m.archived) == 0 {
		return fitLines([]string{"No archived jobs are tracked."}, m.contentHeight(), m.width)
	}

	height := m.contentHeight()
	if m.selectedArchived < 0 {
		m.selectedArchived = 0
	}
	if m.selectedArchived >= len(m.archived) {
		m.selectedArchived = len(m.archived) - 1
	}

	start := m.scroll
	if m.selectedArchived < start {
		start = m.selectedArchived
	}
	if m.selectedArchived >= start+height-3 {
		start = max(0, m.selectedArchived-height+4)
	}
	m.scroll = start

	titleW, companyW, locationW := archivedColumnWidths(m.width)
	refW := 11
	reasonW := 18
	archivedW := 16
	widths := []int{5, reasonW, archivedW, refW, titleW, companyW, locationW}
	lines := []string{
		sectionStyle.Render(fmt.Sprintf("Archived (%d jobs)", len(m.archived))),
		renderTableRow(
			widths,
			[]tableCell{
				{text: "Fit", style: tableHeadStyle, right: true},
				{text: "Reason", style: tableHeadStyle},
				{text: "Archived", style: tableHeadStyle},
				{text: "Ref", style: tableHeadStyle},
				{text: "Title", style: tableHeadStyle},
				{text: "Company", style: tableHeadStyle},
				{text: "Location", style: tableHeadStyle},
			},
			tableHeadStyle,
		),
		dividerStyle.Render(strings.Repeat("─", m.width)),
	}

	end := min(len(m.archived), start+height-len(lines))
	for i := start; i < end; i++ {
		job := m.archived[i]
		score := "-"
		if job.score >= 0 {
			score = strconv.Itoa(job.score)
		}
		cells := []tableCell{
			{text: score, style: scoreStyle(job.score), right: true},
			{text: archiveReasonLabel(job.archiveReason), style: archiveReasonStyle(job.archiveReason)},
			{text: shortTimestamp(job.archivedAt), style: mutedStyle},
			{text: jobReference(job), style: mutedStyle},
			{text: job.title},
			{text: job.company, style: mutedStyle},
			{text: styledLocation(job.location)},
		}
		if i == m.selectedArchived {
			cells = highlightCells(cells)
		}
		line := renderTableRow(widths, cells, lipgloss.NewStyle())
		if i == m.selectedArchived {
			line = selectedRowStyle.Render(padStyledLine(line, m.width))
		}
		lines = append(lines, truncateStyled(line, m.width))
	}

	if m.selectedArchived >= 0 && m.selectedArchived < len(m.archived) {
		job := m.archived[m.selectedArchived]
		lines = append(
			lines,
			"",
			keyValue("Action", "enter details, u unarchive"),
			keyValue("Selected ref", jobReference(job)),
			keyValue("Archive reason", archiveReasonLabel(job.archiveReason)),
			keyValue("Selected URL", shortenURL(job.url)),
		)
	}

	return fitLines(lines, height, m.width)
}

func (m model) jobDetailView() string {
	job, ok := m.detailJob()
	if !ok {
		return fitLines([]string{"Selected job is no longer available."}, m.contentHeight(), m.width)
	}

	boxWidth := max(4, m.width)
	innerWidth := max(1, boxWidth-4)
	innerHeight := max(1, m.contentHeight()-2)
	lines := renderJobDetailLines(job, innerWidth)
	visible := strings.Split(scrollLines(lines, m.detailScroll, innerHeight, innerWidth), "\n")
	title := "Job Report"
	if rowArchived(job) {
		title = "Archived Job Report"
	}
	return renderBox(title, visible, boxWidth)
}

func renderJobDetailLines(job jobRow, width int) []string {
	analysis := job.analysis
	hasAnalysis := jobHasAnalysis(job)
	title := firstNonEmpty(analysis.JobTitle, job.title, job.url)
	company := firstNonEmpty(analysis.Company, job.company, "Unknown")
	score := job.score
	if score < 0 && hasAnalysis {
		score = analysis.FitScore
	}
	scoreText := "-"
	if score >= 0 {
		scoreText = fmt.Sprintf("%d/100", score)
	}
	recommendation := applyText(job)
	if hasAnalysis {
		if analysis.ShouldApply {
			recommendation = "apply"
		} else {
			recommendation = "skip"
		}
	}

	lines := []string{
		sectionStyle.Render(title),
		mutedStyle.Render(company),
		"",
	}
	lines = append(lines, detailField("Status", valueOrDash(job.status), width)...)
	lines = append(lines, detailField("Fit score", scoreText, width)...)
	lines = append(lines, detailField("Recommendation", recommendation, width)...)
	lines = append(lines, detailField("Reference", jobReference(job), width)...)
	lines = append(lines, detailField("Location", valueOrDash(job.location), width)...)
	lines = append(lines, detailField("URL", shortenURL(job.url), width)...)
	if rowArchived(job) {
		lines = append(lines, detailField("Archive reason", archiveReasonLabel(job.archiveReason), width)...)
		lines = append(lines, detailField("Archived", shortTimestamp(job.archivedAt), width)...)
	}

	if job.analysisErr != "" {
		lines = append(lines, "", warnStyle.Render("Analysis"), warnStyle.Render(job.analysisErr))
	}
	if !hasAnalysis {
		lines = append(lines, "", mutedStyle.Render("No analysis is available for this job yet."))
		return lines
	}

	lines = append(lines, detailSection("Summary", width)...)
	lines = append(lines, wrapText(valueOrDash(analysis.Summary), width)...)
	lines = append(lines, detailListSection("Why this fits", analysis.MatchedSkills, width)...)
	lines = append(lines, detailListSection("Gaps", analysis.MissingSkills, width)...)
	lines = append(lines, detailSection("Experience alignment", width)...)
	lines = append(lines, wrapText(valueOrDash(analysis.ExperienceAlignment), width)...)
	lines = append(lines, detailListSection("Concerns", analysis.Concerns, width)...)
	lines = append(lines, detailListSection("Resume Tweaks", analysis.RecommendedResumeTweaks, width)...)
	return lines
}

func detailField(label string, value string, width int) []string {
	labelWidth := min(18, max(10, width/3))
	valueWidth := max(8, width-labelWidth)
	prefix := mutedStyle.Render(padCell(label+":", labelWidth, false))
	wrapped := wrapText(valueOrDash(value), valueWidth)
	if len(wrapped) == 0 {
		return []string{prefix}
	}
	lines := []string{prefix + wrapped[0]}
	for _, line := range wrapped[1:] {
		lines = append(lines, strings.Repeat(" ", labelWidth)+line)
	}
	return lines
}

func detailSection(title string, width int) []string {
	return []string{"", infoStyle.Render(truncatePlain(title, width))}
}

func detailListSection(title string, items []string, width int) []string {
	lines := detailSection(title, width)
	if len(items) == 0 {
		return append(lines, dimStyle.Render("-")+" None noted.")
	}
	for _, item := range items {
		lines = append(lines, wrapBullet(item, width)...)
	}
	return lines
}

func renderBox(title string, lines []string, width int) string {
	innerWidth := max(1, width-4)
	title = " " + title + " "
	topRuleWidth := max(0, width-2-ansi.StringWidth(title))
	top := "┌" + title + strings.Repeat("─", topRuleWidth) + "┐"
	bottom := "└" + strings.Repeat("─", max(0, width-2)) + "┘"

	rendered := []string{truncateStyled(top, width)}
	for _, line := range lines {
		line = truncateStyled(line, innerWidth)
		rendered = append(rendered, "│ "+padStyledLine(line, innerWidth)+" │")
	}
	rendered = append(rendered, truncateStyled(bottom, width))
	return strings.Join(rendered, "\n")
}

func jobHasAnalysis(job jobRow) bool {
	return job.analysisPath != "" && job.analysisErr == ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func wrapText(value string, width int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return []string{""}
	}
	width = max(1, width)
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{""}
	}

	lines := []string{}
	current := ""
	for _, word := range words {
		if ansi.StringWidth(word) > width {
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
			lines = append(lines, splitLongWord(word, width)...)
			continue
		}
		if current == "" {
			current = word
			continue
		}
		candidate := current + " " + word
		if ansi.StringWidth(candidate) <= width {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = word
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func wrapBullet(value string, width int) []string {
	bulletWidth := 2
	wrapped := wrapText(value, max(1, width-bulletWidth))
	if len(wrapped) == 0 {
		return []string{dimStyle.Render("-")}
	}
	lines := []string{dimStyle.Render("-") + " " + wrapped[0]}
	for _, line := range wrapped[1:] {
		lines = append(lines, strings.Repeat(" ", bulletWidth)+line)
	}
	return lines
}

func splitLongWord(value string, width int) []string {
	if width <= 0 {
		return []string{""}
	}
	var lines []string
	for ansi.StringWidth(value) > width {
		piece := ansi.Truncate(value, width, "")
		if piece == "" {
			break
		}
		lines = append(lines, piece)
		value = strings.TrimPrefix(value, piece)
	}
	if value != "" {
		lines = append(lines, value)
	}
	return lines
}

func (m model) reportView() string {
	if len(m.report) == 0 {
		return fitLines([]string{"No report has been generated yet."}, m.contentHeight(), m.width)
	}
	return scrollLines(renderReportLines(m.report, m.width), m.scroll, m.contentHeight(), m.width)
}

func (m model) runView() string {
	lines := []string{}
	if m.run == nil {
		lines = append(lines, "No run has been started from this TUI session.")
		lines = append(lines, "", "Press r for a full run or d for a dry run.")
		return fitLines(lines, m.contentHeight(), m.width)
	}

	mode := "full"
	if m.run.dryRun {
		mode = "dry-run"
	}
	state := "finished"
	if m.run.running {
		state = "running " + spinnerFrames[m.run.spinnerIndex]
	}

	lines = append(lines,
		sectionStyle.Render("Run"),
		fmt.Sprintf("Mode: %s", mode),
		fmt.Sprintf("State: %s", state),
		fmt.Sprintf("Started: %s", formatTime(m.run.startedAt)),
	)
	if !m.run.finishedAt.IsZero() {
		lines = append(lines, fmt.Sprintf("Finished: %s", formatTime(m.run.finishedAt)))
	}
	if m.run.exitErr != "" {
		lines = append(lines, errorStyle.Render("Error: "+m.run.exitErr))
	}
	if len(m.run.summary) > 0 {
		lines = append(lines, "", sectionStyle.Render("Summary"))
		for _, key := range []string{"discovered", "evaluated", "would_evaluate", "cached", "recalculated", "deferred", "skipped_closed", "errors"} {
			lines = append(lines, keyValue(key, strconv.Itoa(m.run.summary[key])))
		}
	}

	lines = append(lines, "", sectionStyle.Render("Log"))
	lines = append(lines, m.run.logs...)
	return scrollLines(lines, m.scroll, m.contentHeight(), m.width)
}

func (m model) contentHeight() int {
	return max(1, m.height-5)
}

func loadDataCmd(appDir string) tea.Cmd {
	return func() tea.Msg {
		env, envErr := loadEnvSummary(appDir)
		jobs, archived, state, stateErr := loadJobs(appDir)
		report, reportErr := loadReport(appDir)

		var errs []string
		for _, err := range []error{envErr, stateErr, reportErr} {
			if err != nil {
				errs = append(errs, err.Error())
			}
		}

		return refreshMsg{
			env:      env,
			jobs:     jobs,
			archived: archived,
			state:    state,
			report:   report,
			err:      strings.Join(errs, "; "),
			loaded:   time.Now(),
		}
	}
}

func startRunCmd(ctx context.Context, appDir string, dryRun bool, ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		go runAnalyzer(ctx, appDir, dryRun, ch)
		return runStartedMsg{dryRun: dryRun}
	}
}

func waitForRunEvent(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return runDoneMsg{}
		}
		return msg
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func setJobArchiveCmd(appDir string, jobID string, archived bool, reason string) tea.Cmd {
	return func() tea.Msg {
		err := setJobArchiveState(appDir, jobID, archived, reason, time.Now())
		msg := archiveDoneMsg{jobID: jobID, archived: archived}
		if err != nil {
			msg.err = err.Error()
		}
		return msg
	}
}

func setJobArchiveState(appDir string, jobID string, archived bool, reason string, now time.Time) error {
	path := filepath.Join(appDir, "state", "jobs.csv")
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("could not read jobs.csv: %w", err)
	}
	records, err := csv.NewReader(file).ReadAll()
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("could not parse jobs.csv: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("could not close jobs.csv: %w", closeErr)
	}
	if len(records) == 0 {
		return fmt.Errorf("jobs.csv is empty")
	}

	headers := append([]string{}, records[0]...)
	headers = ensureCSVField(headers, "archived_at")
	headers = ensureCSVField(headers, "archive_reason")
	header := csvHeader(headers)
	jobIDIndex, ok := header["job_id"]
	if !ok {
		return fmt.Errorf("jobs.csv does not contain job_id")
	}
	archivedAtIndex := header["archived_at"]
	archiveReasonIndex := header["archive_reason"]

	updated := false
	for rowIndex := 1; rowIndex < len(records); rowIndex++ {
		for len(records[rowIndex]) < len(headers) {
			records[rowIndex] = append(records[rowIndex], "")
		}
		if jobIDIndex >= len(records[rowIndex]) || records[rowIndex][jobIDIndex] != jobID {
			continue
		}
		if archived {
			records[rowIndex][archivedAtIndex] = now.Format(time.RFC3339)
			records[rowIndex][archiveReasonIndex] = reason
		} else {
			records[rowIndex][archivedAtIndex] = ""
			records[rowIndex][archiveReasonIndex] = ""
		}
		updated = true
		break
	}
	if !updated {
		return fmt.Errorf("job not found in jobs.csv: %s", jobID)
	}

	tmpPath := path + ".tmp"
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("could not write jobs.csv: %w", err)
	}
	writer := csv.NewWriter(tmpFile)
	if err := writer.Write(headers); err != nil {
		tmpFile.Close()
		return fmt.Errorf("could not write jobs.csv header: %w", err)
	}
	for rowIndex := 1; rowIndex < len(records); rowIndex++ {
		row := normalizeCSVRecord(records[rowIndex], len(headers))
		if err := writer.Write(row); err != nil {
			tmpFile.Close()
			return fmt.Errorf("could not write jobs.csv row: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("could not flush jobs.csv: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("could not close jobs.csv tmp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("could not replace jobs.csv: %w", err)
	}
	return nil
}

func ensureCSVField(headers []string, field string) []string {
	for _, header := range headers {
		if header == field {
			return headers
		}
	}
	return append(headers, field)
}

func csvHeader(headers []string) map[string]int {
	header := map[string]int{}
	for index, field := range headers {
		header[field] = index
	}
	return header
}

func normalizeCSVRecord(record []string, width int) []string {
	normalized := make([]string, width)
	copy(normalized, record)
	return normalized
}

func runAnalyzer(ctx context.Context, appDir string, dryRun bool, ch chan tea.Msg) {
	python := filepath.Join(appDir, ".venv", "bin", "python")
	if _, err := os.Stat(python); err != nil {
		python = "python3"
	}

	args := []string{"-B", filepath.Join(appDir, "analyze_jobs.py"), "--json-events"}
	if dryRun {
		args = append(args, "--dry-run")
	}

	cmd := exec.CommandContext(ctx, python, args...)
	cmd.Dir = appDir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		ch <- runDoneMsg{err: err.Error()}
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		ch <- runDoneMsg{err: err.Error()}
		return
	}

	if err := cmd.Start(); err != nil {
		ch <- runDoneMsg{err: err.Error()}
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go scanAnalyzerOutput(&wg, stdout, ch, false)
	go scanAnalyzerOutput(&wg, stderr, ch, true)

	waitErr := cmd.Wait()
	wg.Wait()

	errText := ""
	if waitErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			errText = "cancelled"
		} else {
			errText = waitErr.Error()
		}
	}
	ch <- runDoneMsg{err: errText}
}

func scanAnalyzerOutput(wg *sync.WaitGroup, reader io.Reader, ch chan tea.Msg, stderr bool) {
	defer wg.Done()
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		raw := scanner.Text()
		event := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			event["type"] = "log"
			event["message"] = raw
			if stderr {
				event["stream"] = "stderr"
			}
		}
		ch <- runEventMsg{event: event, raw: raw}
	}
	if err := scanner.Err(); err != nil {
		ch <- runEventMsg{event: map[string]any{"type": "log", "message": err.Error(), "stream": "stderr"}}
	}
}

func eventLine(msg runEventMsg) string {
	message, _ := msg.event["message"].(string)
	if message == "" {
		message = msg.raw
	}
	typeName, _ := msg.event["type"].(string)
	if typeName == "" || typeName == "log" {
		return message
	}
	return "[" + typeName + "] " + message
}

func eventSummary(event map[string]any) map[string]int {
	summary := map[string]int{}
	for _, key := range []string{"discovered", "evaluated", "would_evaluate", "cached", "recalculated", "deferred", "skipped_closed", "errors"} {
		summary[key] = intFromAny(event[key])
	}
	return summary
}

func appendLog(logs []string, line string) []string {
	logs = append(logs, line)
	if len(logs) > 500 {
		return logs[len(logs)-500:]
	}
	return logs
}

func loadEnvSummary(appDir string) (envSummary, error) {
	values, err := readDotEnv(filepath.Join(appDir, ".env"))
	if err != nil {
		return envSummary{}, err
	}

	return envSummary{
		resumeFile:              values["RESUME_FILE"],
		model:                   values["LLM_MODEL"],
		baseURL:                 values["LLM_BASE_URL"],
		jobURLCount:             countJobURLs(values["JOB_URLS"]),
		maxJobsPerSource:        values["MAX_JOBS_PER_SOURCE"],
		workdayPageSize:         values["WORKDAY_PAGE_SIZE"],
		maxNewEvaluationsPerRun: values["MAX_NEW_EVALUATIONS_PER_RUN"],
		hasAPIKey:               strings.TrimSpace(values["LLM_API_KEY"]) != "" && values["LLM_API_KEY"] != "replace-me",
	}, nil
}

func readDotEnv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}, fmt.Errorf("could not read .env: %w", err)
	}

	values := map[string]string{}
	lines := strings.Split(string(data), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if isMultilineQuotedValue(value) {
			var collected []string
			collected = append(collected, strings.TrimPrefix(value, "\""))
			for i+1 < len(lines) {
				i++
				next := lines[i]
				if strings.HasSuffix(strings.TrimSpace(next), "\"") {
					collected = append(collected, strings.TrimSuffix(next, "\""))
					break
				}
				collected = append(collected, next)
			}
			value = strings.Join(collected, "\n")
		} else {
			value = strings.Trim(value, "\"'")
		}
		values[key] = strings.TrimSpace(value)
	}
	return values, nil
}

func isMultilineQuotedValue(value string) bool {
	return strings.HasPrefix(value, "\"") && (len(value) == 1 || !strings.HasSuffix(value[1:], "\""))
}

func countJobURLs(raw string) int {
	count := 0
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == ','
	}) {
		value := strings.TrimSpace(strings.Trim(item, "\"'"))
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		count++
	}
	return count
}

func loadJobs(appDir string) ([]jobRow, []jobRow, stateSummary, error) {
	path := filepath.Join(appDir, "state", "jobs.csv")
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, stateSummary{}, nil
		}
		return nil, nil, stateSummary{}, fmt.Errorf("could not read jobs.csv: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, nil, stateSummary{}, fmt.Errorf("could not parse jobs.csv: %w", err)
	}
	if len(records) == 0 {
		return nil, nil, stateSummary{}, nil
	}

	header := map[string]int{}
	for i, field := range records[0] {
		header[field] = i
	}

	activeRows := make([]jobRow, 0, len(records)-1)
	archivedRows := make([]jobRow, 0)
	var summary stateSummary
	now := time.Now()
	for _, record := range records[1:] {
		row := jobRow{
			id:            csvValue(record, header, "job_id"),
			title:         csvValue(record, header, "title"),
			company:       csvValue(record, header, "company"),
			location:      csvValue(record, header, "location"),
			status:        csvValue(record, header, "last_evaluation_status"),
			score:         parseScore(csvValue(record, header, "fit_score")),
			apply:         csvValue(record, header, "should_apply"),
			reference:     csvValue(record, header, "job_req_id"),
			url:           csvValue(record, header, "job_url"),
			lastSeen:      csvValue(record, header, "last_seen_at"),
			closedAt:      csvValue(record, header, "closed_at"),
			expiresAt:     csvValue(record, header, "expires_at"),
			archivedAt:    csvValue(record, header, "archived_at"),
			archiveReason: csvValue(record, header, "archive_reason"),
			canApply:      csvValue(record, header, "can_apply"),
			analysisPath:  csvValue(record, header, "analysis_path"),
		}
		if row.title == "" {
			row.title = row.url
		}
		if row.archiveReason == "" {
			row.archiveReason = inferredArchiveReason(row, now)
		}
		if row.archivedAt == "" && row.archiveReason != "" {
			row.archivedAt = inferredArchivedAt(row)
		}
		if row.analysisPath != "" {
			row.analysis, row.analysisErr = loadJobAnalysis(appDir, row.analysisPath)
		}
		summary.total++
		archived := rowArchived(row)
		if archived {
			summary.archived++
			archivedRows = append(archivedRows, row)
		} else {
			summary.active++
			activeRows = append(activeRows, row)
			if row.score >= 0 {
				summary.scored++
			}
			if strings.EqualFold(row.apply, "true") {
				summary.apply++
			}
			switch row.status {
			case "deferred":
				summary.deferred++
			case "cached":
				summary.cached++
			case "evaluated", "recalculated":
				summary.evaluated++
			case "error":
				summary.errors++
			}
		}
		if row.status == "closed" || strings.EqualFold(row.canApply, "false") || row.closedAt != "" {
			summary.closed++
		}
	}

	sort.SliceStable(activeRows, func(i, j int) bool {
		left := activeRows[i]
		right := activeRows[j]
		if left.score != right.score {
			return left.score > right.score
		}
		if left.status != right.status {
			return left.status < right.status
		}
		return left.title < right.title
	})

	sort.SliceStable(archivedRows, func(i, j int) bool {
		left := archivedRows[i]
		right := archivedRows[j]
		if archiveSortTimestamp(left) != archiveSortTimestamp(right) {
			return archiveSortTimestamp(left) > archiveSortTimestamp(right)
		}
		if left.score != right.score {
			return left.score > right.score
		}
		return left.title < right.title
	})

	return activeRows, archivedRows, summary, nil
}

func loadReport(appDir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(appDir, "output", "report.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("could not read report.md: %w", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n"), nil
}

func csvValue(record []string, header map[string]int, field string) string {
	index, ok := header[field]
	if !ok || index < 0 || index >= len(record) {
		return ""
	}
	return record[index]
}

func parseScore(raw string) int {
	if strings.TrimSpace(raw) == "" {
		return -1
	}
	score, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return score
}

func loadJobAnalysis(appDir string, analysisPath string) (jobAnalysis, string) {
	cleaned := filepath.Clean(analysisPath)
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) || cleaned == ".." {
		return jobAnalysis{}, "invalid analysis path"
	}

	data, err := os.ReadFile(filepath.Join(appDir, "state", cleaned))
	if err != nil {
		return jobAnalysis{}, fmt.Sprintf("could not read analysis: %v", err)
	}

	var payload analysisFile
	if err := json.Unmarshal(data, &payload); err != nil {
		return jobAnalysis{}, fmt.Sprintf("could not parse analysis: %v", err)
	}
	return payload.Analysis, ""
}

func rowArchived(job jobRow) bool {
	return strings.TrimSpace(job.archivedAt) != "" || strings.TrimSpace(job.archiveReason) != ""
}

func inferredArchiveReason(job jobRow, now time.Time) string {
	if isExpiredAt(job.expiresAt, now) {
		return "expired"
	}
	if strings.TrimSpace(job.closedAt) != "" || strings.EqualFold(job.status, "closed") || strings.EqualFold(job.canApply, "false") {
		return "closed"
	}
	return ""
}

func inferredArchivedAt(job jobRow) string {
	for _, value := range []string{job.closedAt, job.expiresAt, job.lastSeen} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func archiveSortTimestamp(job jobRow) string {
	for _, value := range []string{job.archivedAt, job.closedAt, job.expiresAt, job.lastSeen} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func isExpiredAt(raw string, now time.Time) bool {
	expires, ok := parseDateInLocation(raw, now.Location())
	return ok && expires.Before(dateOnly(now))
}

func parseDateInLocation(raw string, location *time.Location) (time.Time, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, false
	}

	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-07:00"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return dateOnly(parsed.In(location)), true
		}
	}

	for _, layout := range []string{"2006-01-02", "01/02/2006", "Jan 2, 2006", "January 2, 2006"} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return dateOnly(parsed), true
		}
	}
	return time.Time{}, false
}

func dateOnly(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, value.Location())
}

func applyText(job jobRow) string {
	if strings.EqualFold(job.apply, "true") {
		return "yes"
	}
	if strings.EqualFold(job.apply, "false") {
		return "no"
	}
	return "-"
}

func findAppDir() (string, error) {
	if override := os.Getenv("JOB_GOBLIN_DIR"); override != "" {
		return filepath.Abs(override)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for dir := cwd; ; dir = filepath.Dir(dir) {
		candidates := []string{
			dir,
			filepath.Join(dir, "job_goblin"),
		}
		for _, candidate := range candidates {
			if fileExists(filepath.Join(candidate, "analyze_jobs.py")) {
				return filepath.Abs(candidate)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}

	return "", fmt.Errorf("could not find job_goblin/analyze_jobs.py; set JOB_GOBLIN_DIR")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func keyValue(key string, value string) string {
	return mutedStyle.Render(fmt.Sprintf("%-26s", key+":")) + value
}

func boolText(value bool) string {
	if value {
		return successStyle.Render("yes")
	}
	return errorStyle.Render("no")
}

func jobColumnWidths(width int) (int, int, int) {
	remaining := max(36, width-40)
	title := remaining * 3 / 10
	company := remaining / 5
	location := remaining - title - company

	title = max(14, title)
	company = max(12, company)
	location = max(18, location)

	for title+company+location > remaining {
		switch {
		case title >= company && title > 14:
			title--
		case company > location && company > 12:
			company--
		case location > 18:
			location--
		default:
			title--
		}
	}

	return title, company, location
}

func archivedColumnWidths(width int) (int, int, int) {
	remaining := max(36, width-52)
	title := remaining * 3 / 10
	company := remaining / 5
	location := remaining - title - company

	title = max(14, title)
	company = max(12, company)
	location = max(18, location)

	for title+company+location > remaining {
		switch {
		case title >= company && title > 14:
			title--
		case company > location && company > 12:
			company--
		case location > 18:
			location--
		default:
			title--
		}
	}

	return title, company, location
}

func jobReference(job jobRow) string {
	reference := strings.TrimSpace(job.reference)
	if reference != "" {
		return strings.ToUpper(reference)
	}
	match := jobReferencePattern.FindString(job.url)
	if match == "" {
		return "-"
	}
	return strings.ToUpper(match)
}

func renderTableRow(widths []int, cells []tableCell, rowStyle lipgloss.Style) string {
	parts := make([]string, 0, len(widths))
	for i, width := range widths {
		cell := tableCell{}
		if i < len(cells) {
			cell = cells[i]
		}
		text := padCell(cell.text, width, cell.right)
		parts = append(parts, cell.style.Render(text))
	}
	return rowStyle.Render(strings.Join(parts, " "))
}

func highlightCells(cells []tableCell) []tableCell {
	highlighted := make([]tableCell, len(cells))
	copy(highlighted, cells)
	for index := range highlighted {
		highlighted[index].style = highlighted[index].style.Background(lipgloss.Color("252"))
	}
	return highlighted
}

func padStyledLine(value string, width int) string {
	padding := max(0, width-ansi.StringWidth(value))
	if padding == 0 {
		return value
	}
	return value + strings.Repeat(" ", padding)
}

func padCell(value string, width int, right bool) string {
	value = truncatePlain(value, width)
	padding := max(0, width-ansi.StringWidth(value))
	if right {
		return strings.Repeat(" ", padding) + value
	}
	return value + strings.Repeat(" ", padding)
}

func scoreStyle(score int) lipgloss.Style {
	switch {
	case score >= 80:
		return scoreHighStyle
	case score >= 65:
		return scoreMidStyle
	case score >= 0:
		return scoreLowStyle
	default:
		return dimStyle
	}
}

func jobStatusStyle(status string) lipgloss.Style {
	switch strings.ToLower(status) {
	case "cached":
		return infoStyle
	case "evaluated", "recalculated", "new":
		return successStyle
	case "deferred", "would_defer", "would_evaluate", "would_recalculate":
		return warnStyle
	case "error":
		return errorStyle
	case "closed":
		return dimStyle
	default:
		return mutedStyle
	}
}

func archiveReasonLabel(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "manual":
		return "manual"
	case "expired":
		return "expired"
	case "closed":
		return "closed/non-applyable"
	default:
		return valueOrDash(reason)
	}
}

func archiveReasonStyle(reason string) lipgloss.Style {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "manual":
		return infoStyle
	case "expired":
		return warnStyle
	case "closed":
		return dimStyle
	default:
		return mutedStyle
	}
}

func applyStyle(value string) lipgloss.Style {
	switch strings.ToLower(value) {
	case "true", "yes":
		return successStyle
	case "false", "no":
		return errorStyle
	default:
		return dimStyle
	}
}

func styledLocation(location string) string {
	if strings.TrimSpace(location) == "" {
		return dimStyle.Render("-")
	}

	parts := strings.Split(location, ";")
	rendered := make([]string, 0, len(parts))
	for _, part := range parts {
		text := strings.TrimSpace(part)
		if text == "" {
			continue
		}
		if isUSLocationPart(text) {
			rendered = append(rendered, text)
		} else {
			rendered = append(rendered, warnStyle.Render(text))
		}
	}
	return strings.Join(rendered, "; ")
}

func isUSLocationPart(location string) bool {
	normalized := strings.ToLower(strings.TrimSpace(location))
	return strings.HasPrefix(normalized, "usa") || strings.HasPrefix(normalized, "us ")
}

func statusStyle(status string) lipgloss.Style {
	normalized := strings.ToLower(status)
	switch {
	case strings.Contains(normalized, "fail"), strings.Contains(normalized, "error"):
		return errorStyle
	case strings.Contains(normalized, "cancel"):
		return warnStyle
	case strings.Contains(normalized, "start"), strings.Contains(normalized, "run"):
		return infoStyle
	case strings.Contains(normalized, "complete"), strings.Contains(normalized, "loaded"), strings.Contains(normalized, "ready"):
		return successStyle
	default:
		return mutedStyle
	}
}

func renderReportLines(lines []string, width int) []string {
	rendered := []string{}
	for index := 0; index < len(lines); {
		if isMarkdownTableLine(lines[index]) {
			block := []string{}
			for index < len(lines) && isMarkdownTableLine(lines[index]) {
				block = append(block, lines[index])
				index++
			}
			rendered = append(rendered, renderMarkdownTable(block, width)...)
			continue
		}

		rendered = append(rendered, renderReportLine(lines[index]))
		index++
	}
	return rendered
}

func renderReportLine(line string) string {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(trimmed, "# "):
		return titleStyle.Render(strings.TrimSpace(strings.TrimPrefix(trimmed, "# ")))
	case strings.HasPrefix(trimmed, "## "):
		return sectionStyle.Render(strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")))
	case strings.HasPrefix(trimmed, "### "):
		return infoStyle.Render(strings.TrimSpace(strings.TrimPrefix(trimmed, "### ")))
	case strings.HasPrefix(trimmed, "#### "):
		return mutedStyle.Render(strings.TrimSpace(strings.TrimPrefix(trimmed, "#### ")))
	case strings.HasPrefix(trimmed, "- "):
		return dimStyle.Render("-") + " " + strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
	default:
		return line
	}
}

func isMarkdownTableLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "|") && strings.Count(trimmed, "|") >= 2
}

func renderMarkdownTable(block []string, width int) []string {
	rows := [][]string{}
	for _, line := range block {
		cells := splitMarkdownTableRow(line)
		if len(cells) == 0 || isMarkdownSeparator(cells) {
			continue
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return block
	}

	headers := rows[0]
	if isSummaryTable(headers) {
		return renderSummaryTable(headers, rows[1:], width)
	}
	return renderGenericTable(headers, rows[1:], width)
}

func splitMarkdownTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")

	cells := []string{}
	var current strings.Builder
	escaped := false
	for _, r := range trimmed {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '|' {
			cells = append(cells, strings.TrimSpace(current.String()))
			current.Reset()
			continue
		}
		current.WriteRune(r)
	}
	cells = append(cells, strings.TrimSpace(current.String()))
	return cells
}

func isMarkdownSeparator(cells []string) bool {
	for _, cell := range cells {
		trimmed := strings.TrimSpace(cell)
		if trimmed == "" {
			return false
		}
		for _, r := range trimmed {
			if r != '-' && r != ':' {
				return false
			}
		}
	}
	return true
}

func isSummaryTable(headers []string) bool {
	if len(headers) < 6 {
		return false
	}
	expected := []string{"Fit", "Status", "Apply", "Title", "Company", "URL"}
	for index, header := range expected {
		if index >= len(headers) || !strings.EqualFold(headers[index], header) {
			return false
		}
	}
	return true
}

func renderSummaryTable(headers []string, rows [][]string, width int) []string {
	titleW, companyW, urlW := reportSummaryWidths(width)
	widths := []int{7, 10, 5, titleW, companyW, urlW}
	output := []string{
		renderReportTableRow(widths, headers, tableHeadStyle, true),
		dividerStyle.Render(strings.Repeat("─", min(width, tableWidth(widths)))),
	}

	for _, row := range rows {
		cells := normalizeCells(row, len(widths))
		if len(cells) >= 6 {
			cells[5] = shortenURL(cells[5])
		}
		output = append(output, renderReportTableRow(widths, cells, lipgloss.NewStyle(), false))
	}
	return output
}

func renderGenericTable(headers []string, rows [][]string, width int) []string {
	columnCount := max(1, len(headers))
	widths := equalWidths(width, columnCount)
	output := []string{
		renderReportTableRow(widths, normalizeCells(headers, columnCount), tableHeadStyle, true),
		dividerStyle.Render(strings.Repeat("─", min(width, tableWidth(widths)))),
	}
	for _, row := range rows {
		output = append(output, renderReportTableRow(widths, normalizeCells(row, columnCount), lipgloss.NewStyle(), false))
	}
	return output
}

func reportSummaryWidths(width int) (int, int, int) {
	remaining := max(42, width-32)
	title := remaining / 2
	company := remaining / 4
	urlColumn := remaining - title - company

	title = max(20, title)
	company = max(16, company)
	urlColumn = max(18, urlColumn)

	for title+company+urlColumn > remaining {
		switch {
		case title >= company && title > 20:
			title--
		case company > urlColumn && company > 16:
			company--
		case urlColumn > 18:
			urlColumn--
		default:
			title--
		}
	}
	return title, company, urlColumn
}

func renderReportTableRow(widths []int, values []string, rowStyle lipgloss.Style, header bool) string {
	cells := make([]tableCell, 0, len(widths))
	for index, width := range widths {
		value := ""
		if index < len(values) {
			value = values[index]
		}
		cell := tableCell{text: value}
		if header {
			cell.style = tableHeadStyle
		} else {
			cell.style = reportCellStyle(index, value)
			cell.right = index == 0
		}
		cell.text = truncatePlain(cell.text, width)
		cells = append(cells, cell)
	}
	return truncateStyled(renderTableRow(widths, cells, rowStyle), tableWidth(widths))
}

func reportCellStyle(index int, value string) lipgloss.Style {
	switch index {
	case 0:
		return scoreStyle(parseReportScore(value))
	case 1:
		return jobStatusStyle(value)
	case 2:
		return applyStyle(value)
	default:
		return lipgloss.NewStyle()
	}
}

func parseReportScore(value string) int {
	value = strings.TrimSpace(value)
	if slash := strings.Index(value, "/"); slash >= 0 {
		value = value[:slash]
	}
	score, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return score
}

func normalizeCells(cells []string, count int) []string {
	normalized := make([]string, count)
	for index := 0; index < count && index < len(cells); index++ {
		normalized[index] = cells[index]
	}
	return normalized
}

func equalWidths(width int, columns int) []int {
	gaps := max(0, columns-1)
	available := max(columns, width-gaps)
	base := max(1, available/columns)
	widths := make([]int, columns)
	for index := range widths {
		widths[index] = base
	}
	for index := 0; tableWidth(widths) < width && index < len(widths); index++ {
		widths[index]++
	}
	return widths
}

func tableWidth(widths []int) int {
	total := 0
	for _, width := range widths {
		total += width
	}
	if len(widths) > 0 {
		total += len(widths) - 1
	}
	return total
}

func shortenURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return raw
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	last := ""
	for index := len(parts) - 1; index >= 0; index-- {
		if parts[index] != "" {
			last = parts[index]
			break
		}
	}
	if last == "" {
		return parsed.Host
	}
	return parsed.Host + "/" + last
}

func fitLines(lines []string, height int, width int) string {
	out := make([]string, 0, height)
	for _, line := range lines {
		if len(out) >= height {
			break
		}
		out = append(out, truncateStyled(line, width))
	}
	for len(out) < height {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

func scrollLines(lines []string, scroll int, height int, width int) string {
	if scroll < 0 {
		scroll = 0
	}
	if scroll > max(0, len(lines)-height) {
		scroll = max(0, len(lines)-height)
	}
	return fitLines(lines[scroll:min(len(lines), scroll+height)], height, width)
}

func truncateStyled(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "...")
}

func truncatePlain(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = strings.ReplaceAll(value, "\t", " ")
	if ansi.StringWidth(value) <= width {
		return value
	}
	if width <= 3 {
		return ansi.Truncate(value, width, "")
	}
	return ansi.Truncate(value, width, "...")
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Format("2006-01-02 15:04:05")
}

func shortTimestamp(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-07:00"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.Format("2006-01-02 15:04")
		}
	}
	if len(value) >= len("2006-01-02") {
		return value[:len("2006-01-02")]
	}
	return value
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	default:
		return 0
	}
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
