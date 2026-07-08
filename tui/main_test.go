package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
