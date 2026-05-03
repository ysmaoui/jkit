# Scripting & CI

## JSON Output

All data commands support `--json` for machine-readable output:

```bash
jk list --json | jq '.[].name'
jk status my-app --json | jq '.[0].result'
jk status my-app 42 --json | jq '.url'
```

## Go Template Output

Use `--format` with Go `text/template` syntax:

```bash
# List job names
jk list --format '{{range .}}{{.Name}}{{"\n"}}{{end}}'

# Build results
jk status my-app --format '{{range .}}#{{.Number}} {{.Result}}{{"\n"}}{{end}}'

# Single build
jk status my-app 42 --format '{{.Result}} in {{.Duration}}ms'
```

## Exit Codes

### `jk run --wait`

| Code | Meaning |
|------|---------|
| 0 | SUCCESS |
| 1 | FAILURE |
| 2 | UNSTABLE |
| 3 | ABORTED |
| 4 | Unknown result |

### `jk rebuild --wait`

Same exit codes as `jk run --wait`.

### Other commands

| Command | Exit 0 | Exit 1 |
|---------|--------|--------|
| `auth status` | Auth valid | Auth invalid |
| `lint` | Jenkinsfile valid | Validation errors |
| All others | Success | Error occurred |

## Non-Interactive Auth

For CI pipelines, use flags and env vars to avoid prompts:

```bash
jk auth login --host "$JENKINS_URL" --user "$JENKINS_USER" --token "$JENKINS_TOKEN"
```

Isolate config with `JK_CONFIG_DIR` to avoid conflicts:

```bash
export JK_CONFIG_DIR=$(mktemp -d)
jk auth login --host "$JENKINS_URL" --user "$JENKINS_USER" --token "$JENKINS_TOKEN"
jk run my-app --wait
rm -rf "$JK_CONFIG_DIR"
```

## Debugging

Use `--verbose` to see HTTP requests and responses:

```bash
jk status my-app --verbose
# > GET /job/my-app/api/json
# < 200 OK (45ms)
```

Use `--timeout` for slow Jenkins instances:

```bash
jk log my-app --timeout 60s
```

## Color Behavior

Color is automatically disabled when:
- stdout is not a terminal (piped or redirected)
- `NO_COLOR` env var is set (any value)
- `--no-color` flag is passed

Safe to pipe to files or other tools:

```bash
jk list > jobs.txt           # no color codes in file
jk log my-app | grep ERROR   # no color codes in pipe
```

## CI Recipes

### Trigger and Wait

```bash
#!/bin/bash
set -e

jk auth login --host "$JENKINS_URL" --user "$JENKINS_USER" --token "$JENKINS_TOKEN"

if jk run my-app -p BRANCH="$GIT_BRANCH" --wait; then
    echo "Build passed"
else
    echo "Build failed (exit $?)"
    exit 1
fi
```

### Check Latest Build Status

```bash
result=$(jk status my-app --json | jq -r '.[0].result')
if [ "$result" != "SUCCESS" ]; then
    echo "Latest build: $result"
    exit 1
fi
```

### Validate Jenkinsfile in Pre-Commit

```bash
#!/bin/bash
if [ -f Jenkinsfile ]; then
    jk lint || exit 1
fi
```

### Trigger with Parameters and Stream Log

```bash
jk run my-app \
    -p BRANCH=main \
    -p ENV=production \
    -p VERSION="$(git describe --tags)" \
    --wait --log
```
