package main

import (
	"os"
	"path/filepath"
	"testing"
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
