---
name: stata-remote
description: Run Stata analyses from WSL using a configured Windows Stata Remote service. Use for Stata Remote tasks that submit or revise a do-file, inspect job status or logs, or manage remote project files with rclone.
---

# Stata Remote

Use `stata-remote` for job execution and queries. Use rclone with the `stata-files:` WebDAV remote for project files. Both refer to the same Windows project root. In CLI arguments and `.do` files, use project-relative paths such as `scripts/analysis.do` or `data/panel.dta`. Stata reads existing datasets in place on Windows.

## Connect

Use the configured `STATA_REMOTE_URL` and `STATA_REMOTE_TOKEN`. To use the same token with rclone, set `RCLONE_WEBDAV_BEARER_TOKEN` from `STATA_REMOTE_TOKEN` without displaying its value. `stata-remote jobs` and `rclone lsf stata-files:` check the two interfaces. Use the configured rclone remote name if it differs from `stata-files`. If the service URL is set and a remote is needed, create it with `rclone config create stata-files webdav url "${STATA_REMOTE_URL%/}/dav/" vendor other`. Ask for the service URL or for credentials to be supplied through the environment if they are missing.

## Run a job

1. Find existing inputs with `rclone lsf stata-files:data/` or, when needed, `rclone lsjson stata-files: -R --files-only --include '*.dta'`. Leave large inputs on Windows unless the task calls for a local copy.
2. Use the requested `.do` file or write one locally and upload it along with any dependencies it needs. Choose distinct script paths for simultaneous runs with different contents. Choose a distinct output directory for each concurrent run or rerun whose prior results should be kept.
3. For jobs that write files, use `--output-dir` and have the script read `STATA_REMOTE_OUTPUT_DIR` as a Stata global macro. For jobs that only need logs, omit the flag.

```stata
local outdir : copy global STATA_REMOTE_OUTPUT_DIR
use "data/panel.dta", clear
summarize
save "`outdir'/result.dta", replace
```

Choose script and output paths for the current task. For example, with a local `analysis.do` and `data/panel.dta` already on Windows, run the following.

```bash
rclone copyto analysis.do stata-files:scripts/analysis.do
rclone mkdir stata-files:results/analysis-v1
stata-remote do --output-dir results/analysis-v1 scripts/analysis.do
```

Pass Stata script arguments after the `.do` path. The client prints the job ID to stderr, streams Stata logs to stdout, and exits nonzero on failure. Save the job ID for later queries. When the job succeeds, list outputs with `rclone lsf stata-files:results/analysis-v1/`. Use `rclone cat` for a text output or `rclone copyto` for a specific file needed locally.

## Inspect or recover a job

- With a known job ID, use `stata-remote status "$job_id"` and `stata-remote logs "$job_id"`. Without an ID, use `stata-remote jobs` to find starting or running jobs. Status and logs remain available for completed jobs while the Windows service is running.
- Accepted jobs keep running on Windows after the client disconnects. Query the original ID before resubmitting. If the submission response was lost, retry `do --job-id "$job_id"` with the same script path, arguments, and output directory, so the service can return the original job.
- For a failed job, read its log and check `stata_rc` in the status JSON. A Windows `process_exit_code` of `0` can accompany a Stata error. Correct the script and submit a new job when appropriate. Report the job ID, the status, and the relevant output paths or error.
