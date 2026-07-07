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
R       refresh local state, report, and config
tab     switch views
1-4     jump to Dashboard, Jobs, Report, or Run
arrows  scroll or move through jobs
c       cancel an active run
q       quit
```

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
LLM_MODEL=gpt-5.4
```

Workday search URLs are expanded through Workday's public jobs API. A URL like this is treated as a search source:

```env
JOB_URLS="
https://workday.wd5.myworkdayjobs.com/en-US/Workday/search?q=principal%20engineer
"
```

For compatibility with copied browser URLs, a Workday `/details/...` URL that includes a `q=` parameter is also treated as a search source. To avoid unexpectedly large LLM runs, searches are capped:

```env
MAX_JOBS_PER_SOURCE=20
WORKDAY_PAGE_SIZE=20
MAX_NEW_EVALUATIONS_PER_RUN=10
```

`MAX_JOBS_PER_SOURCE` limits how many postings are fetched from each configured source. `MAX_NEW_EVALUATIONS_PER_RUN` limits how many new or changed jobs are sent to the LLM in one run. If the evaluation cap is reached, the remaining jobs are recorded as deferred and become eligible on a later run.

## State

Job Goblin stores local run state in:

```text
job_goblin/state/jobs.csv
job_goblin/state/analyses/
```

The CSV tracks job IDs, URLs, titles, company, location, content hash, first/last seen timestamps, applyability, fit score, and evaluation metadata. Full LLM analysis payloads are stored as JSON sidecars under `state/analyses/`.

On later runs, unchanged jobs with cached analysis are reused instead of sent to the LLM again. Jobs where `can_apply` is false are marked closed and excluded from the report.
