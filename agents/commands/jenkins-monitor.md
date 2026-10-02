---
description: Monitor a Jenkins build until completion, then report results
allowed-tools: Bash(command:jkit*)
---

Monitor a Jenkins build until it completes, then report results.

Usage:
- `/jenkins-monitor URL`

## Instructions

Given build URL: $ARGUMENTS

1. Run `jkit wait URL --json --max-wait 9m`, with the Bash tool timeout set to
   10 minutes. It returns at once for a finished build and otherwise blocks until
   the build has a result. Read `.result` from the JSON; the exit code matches it
   (0 SUCCESS, 1 FAILURE, 2 UNSTABLE, 3 ABORTED, 4 unknown).
   - Exit 5: still running. Optionally show progress with `jkit log URL --tail 50`,
     then re-run step 1.
   - Exit 1 with nothing on stdout: an error, not a failed build. If it says the
     build is not found, it may still be queued: wait 30s and re-run step 1.
2. Report the result:
   - **FAILURE**: Run `jkit diagnose URL` for failure analysis
   - **UNSTABLE**: Run `jkit test URL --failed` for test failures
   - **SUCCESS**: Report success
   - **ABORTED**: Report aborted
3. Present concise summary: status, duration (`.durationMillis`), root cause (if failed)
