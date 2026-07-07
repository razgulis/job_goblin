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
	screenReport
	screenRun
)

var screenNames = []string{"Dashboard", "Jobs", "Report", "Run"}
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
	appDir      string
	width       int
	height      int
	screen      screen
	scroll      int
	selectedJob int

	env     envSummary
	jobs    []jobRow
	state   stateSummary
	report  []string
	loaded  time.Time
	loadErr string

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
	id        string
	title     string
	company   string
	location  string
	status    string
	score     int
	apply     string
	reference string
	url       string
	lastSeen  string
	closedAt  string
	canApply  string
}

type stateSummary struct {
	total     int
	scored    int
	apply     int
	deferred  int
	cached    int
	evaluated int
	errors    int
	closed    int
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
	env    envSummary
	jobs   []jobRow
	state  stateSummary
	report []string
	err    string
	loaded time.Time
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
		m.state = msg.state
		m.report = msg.report
		m.loaded = msg.loaded
		m.loadErr = msg.err
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
	switch key.String() {
	case "ctrl+c", "q":
		if m.cancelRun != nil {
			m.cancelRun()
		}
		return m, tea.Quit
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
		m.screen = screenReport
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
		return m, nil
	case "end", "G":
		m.scroll = 1_000_000
		if len(m.jobs) > 0 {
			m.selectedJob = len(m.jobs) - 1
		}
		return m, nil
	}

	return m, nil
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
	case screenReport, screenRun:
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
	case screenReport, screenRun:
		m.scroll++
	}
}

func (m *model) pageUp() {
	step := max(1, m.contentHeight()-2)
	switch m.screen {
	case screenJobs:
		m.selectedJob = max(0, m.selectedJob-step)
	case screenReport, screenRun:
		m.scroll = max(0, m.scroll-step)
	}
}

func (m *model) pageDown() {
	step := max(1, m.contentHeight()-2)
	switch m.screen {
	case screenJobs:
		m.selectedJob = min(max(0, len(m.jobs)-1), m.selectedJob+step)
	case screenReport, screenRun:
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
	case screenReport:
		body = m.reportView()
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
		keyValue("Scored jobs", strconv.Itoa(m.state.scored)),
		keyValue("Recommended apply", successStyle.Render(strconv.Itoa(m.state.apply))),
		keyValue("Deferred", warnStyle.Render(strconv.Itoa(m.state.deferred))),
		keyValue("Cached", infoStyle.Render(strconv.Itoa(m.state.cached))),
		keyValue("Evaluated", successStyle.Render(strconv.Itoa(m.state.evaluated))),
		keyValue("Errors", errorStyle.Render(strconv.Itoa(m.state.errors))),
		keyValue("Closed/non-applyable", mutedStyle.Render(strconv.Itoa(m.state.closed))),
		"",
		sectionStyle.Render("Files"),
		keyValue("Report lines", strconv.Itoa(len(m.report))),
		keyValue("Loaded", formatTime(m.loaded)),
	}

	if m.loadErr != "" {
		lines = append(lines, "", errorStyle.Render("Load Error"), errorStyle.Render(m.loadErr))
	}

	return fitLines(lines, m.contentHeight(), m.width)
}

func (m model) jobsView() string {
	if len(m.jobs) == 0 {
		return fitLines([]string{"No jobs are tracked yet."}, m.contentHeight(), m.width)
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
		sectionStyle.Render(fmt.Sprintf("Jobs (%d tracked)", len(m.jobs))),
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
			keyValue("Selected ref", jobReference(job)),
			keyValue("Selected locations", valueOrDash(job.location)),
			keyValue("Selected URL", shortenURL(job.url)),
		)
	}

	return fitLines(lines, height, m.width)
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
		jobs, state, stateErr := loadJobs(appDir)
		report, reportErr := loadReport(appDir)

		var errs []string
		for _, err := range []error{envErr, stateErr, reportErr} {
			if err != nil {
				errs = append(errs, err.Error())
			}
		}

		return refreshMsg{
			env:    env,
			jobs:   jobs,
			state:  state,
			report: report,
			err:    strings.Join(errs, "; "),
			loaded: time.Now(),
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

func loadJobs(appDir string) ([]jobRow, stateSummary, error) {
	path := filepath.Join(appDir, "state", "jobs.csv")
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, stateSummary{}, nil
		}
		return nil, stateSummary{}, fmt.Errorf("could not read jobs.csv: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, stateSummary{}, fmt.Errorf("could not parse jobs.csv: %w", err)
	}
	if len(records) == 0 {
		return nil, stateSummary{}, nil
	}

	header := map[string]int{}
	for i, field := range records[0] {
		header[field] = i
	}

	rows := make([]jobRow, 0, len(records)-1)
	var summary stateSummary
	for _, record := range records[1:] {
		row := jobRow{
			id:        csvValue(record, header, "job_id"),
			title:     csvValue(record, header, "title"),
			company:   csvValue(record, header, "company"),
			location:  csvValue(record, header, "location"),
			status:    csvValue(record, header, "last_evaluation_status"),
			score:     parseScore(csvValue(record, header, "fit_score")),
			apply:     csvValue(record, header, "should_apply"),
			reference: csvValue(record, header, "job_req_id"),
			url:       csvValue(record, header, "job_url"),
			lastSeen:  csvValue(record, header, "last_seen_at"),
			closedAt:  csvValue(record, header, "closed_at"),
			canApply:  csvValue(record, header, "can_apply"),
		}
		rows = append(rows, row)
		summary.total++
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
		if row.status == "closed" || strings.EqualFold(row.canApply, "false") || row.closedAt != "" {
			summary.closed++
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		left := rows[i]
		right := rows[j]
		if left.score != right.score {
			return left.score > right.score
		}
		if left.status != right.status {
			return left.status < right.status
		}
		return left.title < right.title
	})

	return rows, summary, nil
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
