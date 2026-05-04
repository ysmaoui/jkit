---
description: Monitor a Jenkins build until completion, then report results
allowed-tools: Bash(command:jk*)
---

Monitor a Jenkins build until it completes, then report results.

Usage:
- `/jenkins-monitor URL`

## Instructions

Given build URL: $ARGUMENTS

1. Check current status: `jk status URL --json`
2. Based on status:
   - **Building**: Stream log with `jk log -f URL`, then get final status with `jk status URL`
   - **Queued**: Wait 30s, re-check `jk status URL --json`, repeat until building, then stream
   - **Finished**: Skip to step 3
3. On completion, report result:
   - **FAILURE**: Run `jk diagnose URL` for failure analysis
   - **UNSTABLE**: Run `jk test URL --failed` for test failures
   - **SUCCESS**: Report success
   - **ABORTED**: Report aborted
4. Present concise summary: status, duration, root cause (if failed)
