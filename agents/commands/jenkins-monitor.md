---
description: Monitor a Jenkins build until completion, then report results
allowed-tools: Bash(command:jkit*)
---

Monitor a Jenkins build until it completes, then report results.

Usage:
- `/jenkins-monitor URL`

## Instructions

Given build URL: $ARGUMENTS

1. Check current status: `jkit status URL --json`
2. Based on status:
   - **Building**: Stream log with `jkit log -f URL`, then get final status with `jkit status URL`
   - **Queued**: Wait 30s, re-check `jkit status URL --json`, repeat until building, then stream
   - **Finished**: Skip to step 3
3. On completion, report result:
   - **FAILURE**: Run `jkit diagnose URL` for failure analysis
   - **UNSTABLE**: Run `jkit test URL --failed` for test failures
   - **SUCCESS**: Report success
   - **ABORTED**: Report aborted
4. Present concise summary: status, duration, root cause (if failed)
