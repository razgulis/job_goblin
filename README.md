# Job Goblin

Job Goblin is a local prototype that compares a Markdown resume against one or more job postings and writes a Markdown fit report.

## Setup

Create a virtual environment and install dependencies:

```bash
cd job_goblin
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
```

Copy the example environment file:

```bash
cp .env.example .env
```

Put resume files in:

```text
job_goblin/resume/
```

Set `RESUME_FILE` in `.env` to the resume filename you want to use. The value must be a filename under `job_goblin/resume/`, not a path.

## Run

From the repository root:

```bash
python3 job_goblin/analyze_jobs.py
```

Or from this directory:

```bash
python3 analyze_jobs.py
```

The report is written to:

```text
job_goblin/output/report.md
```

Preview a run without LLM calls or file writes:

```bash
python3 job_goblin/analyze_jobs.py --dry-run
```

The script also supports newline-delimited JSON events for terminal or service wrappers:

```bash
python3 job_goblin/analyze_jobs.py --json-events
```

## Terminal UI

Job Goblin includes a Bubble Tea terminal interface under `job_goblin/tui`.

Run it from the repository root:

```bash
cd job_goblin/tui
go run .
```

Useful keys:

```text
r       run the full analyzer
d       run the analyzer in dry-run mode
R       refresh local state and config
tab     switch views
1-5     jump to Dashboard, Jobs, Archived, Run, or Settings
arrows  scroll or move through jobs
enter   show the selected job report from Jobs or Archived
esc     close the selected job report
a       archive the selected active job from Jobs
u       unarchive the selected job from Archived
c       cancel an active run
q       quit
```

The Settings tab edits the resume, job sources, LLM connection, and run limits. Press
`enter` to edit a field and `s` to save changes to `.env`. The API key is masked.
When `.env` does not exist, the TUI opens Settings on startup. A run with missing
required settings is redirected there instead of starting the analyzer.

On `Job sources`, `enter` expands a one-URL-per-row editor in place with an empty
add row at the top. On `Resume file`, `enter` browses the Markdown files under
`resume/`. The API mode and reasoning effort fields open an in-place list of
supported values.

To build a reusable local binary:

```bash
cd job_goblin/tui
go build -o job-goblin-tui .
```

## Configuration

`JOB_URLS` can contain one URL per line or comma-separated URLs. Newline-separated URLs are easier to read:

```env
JOB_URLS="
https://jobs.example.com/platform-engineer
https://boards.greenhouse.io/example/jobs/123456
https://jobs.lever.co/example/abc-def
"
```

The LLM settings use an OpenAI-compatible API:

```env
LLM_BASE_URL=https://api.openai.com/v1
LLM_API_KEY=replace-me
LLM_MODEL=gpt-5.6-terra
LLM_API_MODE=responses
LLM_REASONING_EFFORT=medium
```

`responses` sends the reasoning setting as `reasoning.effort`, uses strict
structured output, and disables Responses application-state storage. Use
`chat_completions` for an OpenAI-compatible provider that does not implement
`/responses`; that mode sends `reasoning_effort`. Set the effort to `default`
to omit the reasoning parameter for a provider or model that does not support
it. Changing the model, provider, API mode, reasoning effort, resume, or
evaluation prompt invalidates cached job scores.

Workday search URLs are expanded through Workday's public jobs API. A URL like this is treated as a search source:

```env
JOB_URLS="
https://workday.wd5.myworkdayjobs.com/en-US/Workday/search?q=principal%20engineer
"
```

For compatibility with copied browser URLs, a Workday `/details/...` URL that includes a `q=` parameter is also treated as a search source. To avoid unexpectedly large LLM runs, searches are capped:

```env
MAX_JOBS_PER_SOURCE=100
WORKDAY_PAGE_SIZE=20
MAX_NEW_EVALUATIONS_PER_RUN=40
```

`MAX_JOBS_PER_SOURCE` limits how many postings are fetched from each configured source. `MAX_NEW_EVALUATIONS_PER_RUN` limits how many new or changed jobs are sent to the LLM in one run. If the evaluation cap is reached, the remaining jobs are recorded as deferred and become eligible on a later run.

## State

Job Goblin stores local run state in:

```text
job_goblin/state/jobs.csv
job_goblin/state/analyses/
```

The CSV tracks job IDs, URLs, titles, company, location, content hash, first/last seen timestamps, applyability, archive status, fit score, and evaluation metadata. Full LLM analysis payloads are stored as JSON sidecars under `state/analyses/`.

On later runs, unchanged active jobs with cached analysis are reused instead of sent to the LLM again. Manually archived, expired, closed, and non-applyable jobs are excluded from active scraping and evaluation and are visible in the Archived tab.
