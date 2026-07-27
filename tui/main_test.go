package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestReadDotEnvMultilineQuotedValue(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := `RESUME_FILE="candidate.md"

JOB_URLS="
https://jobs.example.com/one
https://jobs.example.com/two
"

LLM_MODEL=gpt-5.4
`
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	values, err := readDotEnv(envPath)
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}

	if got := countJobURLs(values["JOB_URLS"]); got != 2 {
		t.Fatalf("countJobURLs() = %d, want 2; raw value: %q", got, values["JOB_URLS"])
	}
}

func TestReadDotEnvInlineQuotedValue(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := `JOB_URLS="https://jobs.example.com/one,https://jobs.example.com/two"`
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	values, err := readDotEnv(envPath)
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}

	if got := countJobURLs(values["JOB_URLS"]); got != 2 {
		t.Fatalf("countJobURLs() = %d, want 2; raw value: %q", got, values["JOB_URLS"])
	}
}

func TestLoadEnvSummaryUsesFirstRunDefaultsWithoutEnvFile(t *testing.T) {
	summary, err := loadEnvSummary(t.TempDir())
	if err != nil {
		t.Fatalf("loadEnvSummary: %v", err)
	}
	if summary.exists {
		t.Fatal("summary.exists = true, want false")
	}

	want := map[string]string{
		"SCRAPE_TIMEOUT_SECONDS":      "20",
		"LLM_TIMEOUT_SECONDS":         "60",
		"MAX_JOBS_PER_SOURCE":         "100",
		"WORKDAY_PAGE_SIZE":           "20",
		"MAX_NEW_EVALUATIONS_PER_RUN": "40",
	}
	for key, expected := range want {
		if got := summary.values[key]; got != expected {
			t.Errorf("%s = %q, want %q", key, got, expected)
		}
	}
}

func TestWriteDotEnvValuesPreservesUnknownContentAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	writeFile(t, envPath, `# Keep this comment.
CUSTOM_SETTING=keep-me
RESUME_FILE=old.md
JOB_URLS="
https://jobs.example.com/old
"
LLM_API_KEY=old-key
`)

	values := defaultSettingsValues()
	values["RESUME_FILE"] = "candidate.md"
	values["JOB_URLS"] = "https://jobs.example.com/one, https://jobs.example.com/two"
	values["LLM_API_KEY"] = "new-key"
	if err := writeDotEnvValues(envPath, values); err != nil {
		t.Fatalf("writeDotEnvValues: %v", err)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read updated .env: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "# Keep this comment.") || !strings.Contains(content, "CUSTOM_SETTING=keep-me") {
		t.Fatalf("unmanaged .env content was not preserved:\n%s", content)
	}

	roundTrip, err := readDotEnv(envPath)
	if err != nil {
		t.Fatalf("readDotEnv after update: %v", err)
	}
	if got := roundTrip["RESUME_FILE"]; got != "candidate.md" {
		t.Errorf("RESUME_FILE = %q, want candidate.md", got)
	}
	if got := countJobURLs(roundTrip["JOB_URLS"]); got != 2 {
		t.Errorf("JOB_URLS count = %d, want 2", got)
	}
	if got := roundTrip["LLM_API_KEY"]; got != "new-key" {
		t.Errorf("LLM_API_KEY = %q, want new-key", got)
	}
}

func TestFullRunRedirectsToSettingsWhenAPIKeyIsMissing(t *testing.T) {
	values := defaultSettingsValues()
	values["RESUME_FILE"] = "candidate.md"
	values["JOB_URLS"] = "https://jobs.example.com/search"
	env := envSummary{exists: true, values: values}
	m := model{
		screen:   screenDashboard,
		env:      env,
		settings: newSettingsForm(env),
	}

	updated, cmd := startRunIfConfigured(m, false)
	got := updated.(model)
	if cmd != nil {
		t.Fatal("startRunIfConfigured returned a command for incomplete configuration")
	}
	if got.screen != screenSettings {
		t.Fatalf("screen = %v, want Settings", got.screen)
	}
	if !strings.Contains(got.settings.err, "API key") {
		t.Fatalf("settings error = %q, want API key guidance", got.settings.err)
	}
}

func TestMissingEnvOpensSettingsOnInitialRefresh(t *testing.T) {
	m := initialModel(t.TempDir())
	env := envSummary{exists: false, values: defaultSettingsValues()}

	updated, _ := m.Update(refreshMsg{env: env, loaded: time.Now()})
	got := updated.(model)
	if got.screen != screenSettings {
		t.Fatalf("screen = %v, want Settings", got.screen)
	}
	if !strings.Contains(got.status, "Configure settings") {
		t.Fatalf("status = %q, want configuration prompt", got.status)
	}
}

func TestSettingsViewMasksAPIKeyAndKeepsSelectedFieldVisible(t *testing.T) {
	values := defaultSettingsValues()
	values["LLM_API_KEY"] = "secret-value"
	env := envSummary{exists: true, values: values}
	form := newSettingsForm(env)
	form.selectKey("MAX_NEW_EVALUATIONS_PER_RUN")
	m := model{
		appDir:   "/tmp/job-goblin",
		width:    100,
		height:   14,
		screen:   screenSettings,
		env:      env,
		settings: form,
	}

	view := ansi.Strip(m.settingsView())
	if strings.Contains(view, "secret-value") {
		t.Fatalf("settings view exposed the API key:\n%s", view)
	}
	if !strings.Contains(view, "Max evaluations/run") {
		t.Fatalf("selected field was not visible in a short terminal:\n%s", view)
	}
}

func TestSettingsEditingAcceptsGlobalShortcutCharacters(t *testing.T) {
	env := envSummary{values: defaultSettingsValues()}
	form := newSettingsForm(env)
	form.selectKey("LLM_API_KEY")
	form.editing = true
	form.cursor = len([]rune(form.fields[form.selected].value))
	form.editStartValue = form.fields[form.selected].value
	m := model{screen: screenSettings, env: env, settings: form}

	updated, _ := updateKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("qrd")})
	got := updated.(model)
	if value := got.settings.fields[got.settings.selected].value; value != "qrd" {
		t.Fatalf("edited API key = %q, want qrd", value)
	}
}

func TestJobSourcesExpandInlineWithTopAddEntry(t *testing.T) {
	values := defaultSettingsValues()
	values["JOB_URLS"] = "https://jobs.example.com/one, https://work.example.com/two"
	env := envSummary{exists: true, values: values}
	form := newSettingsForm(env)
	form.selectKey("JOB_URLS")
	m := model{
		appDir:   "/tmp/job-goblin",
		width:    100,
		height:   20,
		screen:   screenSettings,
		env:      env,
		settings: form,
	}

	collapsed := ansi.Strip(m.settingsView())
	if !strings.Contains(collapsed, "2 URLs") {
		t.Fatalf("collapsed settings did not show a source count:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "https://jobs.example.com/one") {
		t.Fatalf("collapsed settings still rendered sources as one scalar value:\n%s", collapsed)
	}

	updated, _, handled := updateSettingsNavigation(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !handled || !updated.settings.sourceExpanded {
		t.Fatal("Enter did not expand Job sources")
	}
	view := ansi.Strip(updated.settingsView())
	for _, expected := range []string{"> +", "https://jobs.example.com/one", "https://work.example.com/two", "LLM base URL"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("expanded source view missing %q:\n%s", expected, view)
		}
	}
}

func TestSelectedSettingRowHighlightsLabelValueAndFullWidth(t *testing.T) {
	field := settingsField{key: "LLM_MODEL", label: "LLM model", value: "gpt-test"}
	row := renderSettingRow(field, true, false, 0, 24, 60, 100)
	plain := ansi.Strip(row)

	if !strings.Contains(plain, "LLM model") || !strings.Contains(plain, "gpt-test") {
		t.Fatalf("selected row did not contain its label and value: %q", plain)
	}
	if got := ansi.StringWidth(row); got != 100 {
		t.Fatalf("selected row width = %d, want 100", got)
	}
}

func TestJobSourceAddRowPrependsNewURLAndRemainsEmpty(t *testing.T) {
	values := defaultSettingsValues()
	values["JOB_URLS"] = "https://jobs.example.com/existing"
	env := envSummary{exists: true, values: values}
	form := newSettingsForm(env)
	form.selectKey("JOB_URLS")
	form.sourceExpanded = true
	m := model{screen: screenSettings, env: env, settings: form}

	m, _, _ = updateSourceNavigation(m, tea.KeyMsg{Type: tea.KeyEnter})
	edited, _ := updateSourceEditKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("https://jobs.example.com/new")})
	committed, _ := updateSourceEditKey(edited.(model), tea.KeyMsg{Type: tea.KeyEnter})
	got := committed.(model)

	urls := got.settings.sourceURLs()
	if len(urls) != 2 || urls[0] != "https://jobs.example.com/new" {
		t.Fatalf("source URLs = %#v, want new URL prepended", urls)
	}
	if got.settings.sourceInput != "" || got.settings.sourceSelected != 0 {
		t.Fatalf("add row was not reset: input=%q selected=%d", got.settings.sourceInput, got.settings.sourceSelected)
	}
}

func TestResumeBrowserListsMarkdownFilesAndSelectsFilename(t *testing.T) {
	dir := t.TempDir()
	resumeDir := filepath.Join(dir, "resume")
	if err := os.MkdirAll(resumeDir, 0o755); err != nil {
		t.Fatalf("mkdir resume: %v", err)
	}
	writeFile(t, filepath.Join(resumeDir, "second.md"), "second")
	writeFile(t, filepath.Join(resumeDir, "first.md"), "first")
	writeFile(t, filepath.Join(resumeDir, "ignored.txt"), "ignored")
	writeFile(t, filepath.Join(resumeDir, "ignored.MD"), "ignored")

	env := envSummary{exists: true, values: defaultSettingsValues()}
	form := newSettingsForm(env)
	form.selectKey("RESUME_FILE")
	m := model{appDir: dir, screen: screenSettings, env: env, settings: form}

	opened, _, handled := updateSettingsNavigation(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !handled || !opened.settings.resumeBrowsing {
		t.Fatal("Enter did not open the resume browser")
	}
	if got := opened.settings.resumeFiles; len(got) != 2 || got[0] != "first.md" || got[1] != "second.md" {
		t.Fatalf("resume files = %#v, want sorted Markdown files", got)
	}

	selected, _, _ := updateResumeBrowser(opened, tea.KeyMsg{Type: tea.KeyEnter})
	if value := selected.settings.fields[selected.settings.selected].value; value != "first.md" {
		t.Fatalf("selected resume = %q, want first.md", value)
	}
	if !selected.settings.dirty {
		t.Fatal("selecting a resume did not mark settings dirty")
	}
}

func TestJobReferencePrefersStateValue(t *testing.T) {
	job := jobRow{
		reference: "JR-0108404",
		url:       "https://example.com/job/Other_JR-9999999",
	}

	if got := jobReference(job); got != "JR-0108404" {
		t.Fatalf("jobReference() = %q, want JR-0108404", got)
	}
}

func TestJobReferenceFallsBackToURL(t *testing.T) {
	job := jobRow{
		url: "https://example.com/job/Senior-Engineer_JR-0108404",
	}

	if got := jobReference(job); got != "JR-0108404" {
		t.Fatalf("jobReference() = %q, want JR-0108404", got)
	}
}

func TestIsUSLocationPart(t *testing.T) {
	if !isUSLocationPart("USA, GA, Atlanta") {
		t.Fatal("expected USA location to be treated as US")
	}

	if isUSLocationPart("Canada, ON, Toronto") {
		t.Fatal("expected Canada location to be treated as non-US")
	}
}

func TestLoadJobsSplitsActiveAndArchivedRows(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	analysisDir := filepath.Join(stateDir, "analyses")
	if err := os.MkdirAll(analysisDir, 0o755); err != nil {
		t.Fatalf("mkdir analyses: %v", err)
	}

	writeFile(t, filepath.Join(analysisDir, "active.json"), `{
  "analysis": {
    "job_title": "Active Analyzed Job",
    "company": "Example Co",
    "fit_score": 91,
    "should_apply": true,
    "summary": "Good fit."
  }
}`)
	writeFile(t, filepath.Join(stateDir, "jobs.csv"), `job_id,source_url,job_url,title,company,location,job_req_id,posted_on,start_date,expires_at,content_hash,first_seen_at,last_seen_at,closed_at,archived_at,archive_reason,can_apply,fit_score,should_apply,last_evaluated_at,analysis_path,model,last_evaluation_status
active-1,https://example.com/a,https://example.com/a,Active Job,Example Co,USA Remote,JR-1,,,,hash,2026-07-01,2026-07-07,,,,true,91,true,2026-07-07,analyses/active.json,gpt-5.4,cached
manual-1,https://example.com/m,https://example.com/m,Manual Job,Example Co,USA Remote,JR-2,,,,hash,2026-07-01,2026-07-07,,2026-07-07T12:00:00-06:00,manual,true,72,true,2026-07-07,,gpt-5.4,cached
closed-1,https://example.com/c,https://example.com/c,Closed Job,Example Co,USA Remote,JR-3,,,,hash,2026-07-01,2026-07-07,2026-07-07T12:00:00-06:00,,,false,44,false,2026-07-07,,gpt-5.4,closed
`)

	active, archived, summary, err := loadJobs(dir)
	if err != nil {
		t.Fatalf("loadJobs: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active jobs = %d, want 1", len(active))
	}
	if len(archived) != 2 {
		t.Fatalf("archived jobs = %d, want 2", len(archived))
	}
	if summary.total != 3 || summary.active != 1 || summary.archived != 2 {
		t.Fatalf("summary = %+v, want total=3 active=1 archived=2", summary)
	}
	if active[0].analysis.JobTitle != "Active Analyzed Job" {
		t.Fatalf("analysis title = %q, want Active Analyzed Job", active[0].analysis.JobTitle)
	}
	if archived[0].archiveReason != "manual" {
		t.Fatalf("first archived reason = %q, want manual", archived[0].archiveReason)
	}
	if archived[1].archiveReason != "closed" {
		t.Fatalf("second archived reason = %q, want closed", archived[1].archiveReason)
	}
}

func TestJobsViewShowsExpirationColumn(t *testing.T) {
	m := model{
		width:  140,
		height: 30,
		screen: screenJobs,
		loaded: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		jobs: []jobRow{
			{
				id:        "job-1",
				title:     "Platform Engineer",
				company:   "Example Co",
				location:  "USA Remote",
				status:    "evaluated",
				score:     92,
				apply:     "true",
				reference: "JR-1",
				url:       "https://example.com/jobs/1",
				expiresAt: "2026-08-15",
			},
		},
	}

	view := ansi.Strip(m.jobsView())
	if !strings.Contains(view, "Expires") {
		t.Fatalf("jobs view did not include Expires header:\n%s", view)
	}
	if !strings.Contains(view, "2026-08-15") {
		t.Fatalf("jobs view did not include expiration date:\n%s", view)
	}
}

func TestRunScrollUpClampsFromBottom(t *testing.T) {
	m := model{
		width:  100,
		height: 15,
		screen: screenRun,
		scroll: 1_000_000,
		run: &runState{
			startedAt: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
			logs:      make([]string, 40),
		},
	}
	for i := range m.run.logs {
		m.run.logs[i] = "line"
	}

	maxScroll := m.maxRunScroll()
	if maxScroll == 0 {
		t.Fatal("test setup did not create scrollable run output")
	}

	m.moveUp()
	if got, want := m.scroll, maxScroll-1; got != want {
		t.Fatalf("scroll after moveUp = %d, want %d", got, want)
	}
}

func TestDetailPageUpClampsFromBottom(t *testing.T) {
	m := model{
		width:        80,
		height:       12,
		screen:       screenJobs,
		detailOpen:   true,
		detailJobID:  "job-1",
		detailScroll: 1_000_000,
		jobs: []jobRow{
			{
				id:           "job-1",
				title:        "Platform Engineer",
				company:      "Example Co",
				location:     "USA Remote",
				status:       "evaluated",
				score:        90,
				apply:        "true",
				url:          "https://example.com/jobs/1",
				analysisPath: "analyses/job-1.json",
				analysis: jobAnalysis{
					Summary:             strings.Repeat("summary ", 120),
					ExperienceAlignment: strings.Repeat("alignment ", 120),
					MatchedSkills:       []string{strings.Repeat("matched ", 80)},
					MissingSkills:       []string{strings.Repeat("missing ", 80)},
					Concerns:            []string{strings.Repeat("concern ", 80)},
				},
			},
		},
	}

	maxScroll := m.maxDetailScroll()
	if maxScroll == 0 {
		t.Fatal("test setup did not create scrollable detail output")
	}

	updated, _ := updateDetailKey(m, tea.KeyMsg{Type: tea.KeyPgUp})
	got := updated.(model).detailScroll
	step := max(1, m.contentHeight()-4)
	want := max(0, maxScroll-step)
	if got != want {
		t.Fatalf("detailScroll after pgup = %d, want %d", got, want)
	}
}

func TestScoringHealthTracksCoverageAndFreshness(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	withUnscored := stateSummary{
		active:          4,
		scored:          3,
		latestEvaluated: now.Add(-2 * time.Hour),
	}
	if got := scoringHealthPercent(withUnscored, now); got != 75 {
		t.Fatalf("scoringHealthPercent with unscored job = %d, want 75", got)
	}

	stale := stateSummary{
		active:          4,
		scored:          4,
		latestEvaluated: now.Add(-48 * time.Hour),
	}
	if got := scoringHealthPercent(stale, now); got != 50 {
		t.Fatalf("scoringHealthPercent with stale scoring = %d, want 50", got)
	}
}

func TestSetJobArchiveStateTogglesArchiveFields(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	writeFile(t, filepath.Join(stateDir, "jobs.csv"), `job_id,source_url,job_url,title,company,location,job_req_id,posted_on,start_date,expires_at,content_hash,first_seen_at,last_seen_at,closed_at,can_apply,fit_score,should_apply,last_evaluated_at,analysis_path,model,last_evaluation_status
job-1,https://example.com/1,https://example.com/1,Job One,Example Co,USA Remote,JR-1,,,,hash,2026-07-01,2026-07-07,,true,80,true,2026-07-07,,gpt-5.4,cached
`)

	when := time.Date(2026, 7, 8, 9, 30, 0, 0, time.FixedZone("MDT", -6*60*60))
	if err := setJobArchiveState(dir, "job-1", true, "manual", when); err != nil {
		t.Fatalf("archive job: %v", err)
	}
	_, archived, _, err := loadJobs(dir)
	if err != nil {
		t.Fatalf("load archived jobs: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("archived jobs = %d, want 1", len(archived))
	}
	if archived[0].archiveReason != "manual" {
		t.Fatalf("archive reason = %q, want manual", archived[0].archiveReason)
	}

	if err := setJobArchiveState(dir, "job-1", false, "", when); err != nil {
		t.Fatalf("unarchive job: %v", err)
	}
	active, archived, _, err := loadJobs(dir)
	if err != nil {
		t.Fatalf("load unarchived jobs: %v", err)
	}
	if len(active) != 1 || len(archived) != 0 {
		t.Fatalf("active=%d archived=%d, want active=1 archived=0", len(active), len(archived))
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
