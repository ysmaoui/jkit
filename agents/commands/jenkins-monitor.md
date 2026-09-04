---
description: Monitor a Jenkins build until completion, then report results
allowed-tools: Bash(command:jkit*)
---

Monitor a Jenkins build until it completes, then report results.

Usage:
- `/jenkins-monitor URL`

## Instructions

Given build URL: $ARGUMENTS

1. Check current status: `jkit status URL --json`. The build is finished only when
   `.building` is false. Ignore `.result` while `.building` is true: Jenkins reports
   a result on in-progress builds, so a running build can read `SUCCESS`.
2. Based on status:
   - **Building**: Stream log with `jkit log -f URL`. The stream ends when Jenkins
     stops sending log data, which can happen before the build is finalized, so
     afterwards poll `jkit status URL --json` every 15s until `.building` is false.
   - **Queued**: Wait 30s, re-check `jkit status URL --json`, repeat until building, then stream
   - **Finished**: Skip to step 3
3. Once `.building` is false, report `.result`:
   - **FAILURE**: Run `jkit diagnose URL` for failure analysis
   - **UNSTABLE**: Run `jkit test URL --failed` for test failures
   - **SUCCESS**: Report success
   - **ABORTED**: Report aborted
4. Present concise summary: status, duration, root cause (if failed)
