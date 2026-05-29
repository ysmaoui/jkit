# Scripting & CI

## JSON Output

All data commands support `--json` for machine-readable output:

```bash
jkit list --json | jq '.[].name'
jkit status my-app --json | jq '.[0].result'
jkit status my-app 42 --json | jq '.url'

# Resolve a stage's node ID by its qualified path, then fetch just that log
id=$(jkit stages my-app 42 --json | jq -r '.[] | select(.path=="Linux/Test") | .id')
jkit log my-app 42 --stage-id "$id"
```

## Go Template Output

Use `--format` with Go `text/template` syntax:

```bash
# List job names
jkit list --format '{{range .}}{{.Name}}{{"\n"}}{{end}}'

# Build results
jkit status my-app --format '{{range .}}#{{.Number}} {{.Result}}{{"\n"}}{{end}}'

# Single build
jkit status my-app 42 --format '{{.Result}} in {{.Duration}}ms'
```

## Exit Codes

### `jkit run --wait`

| Code | Meaning |
|------|---------|
| 0 | SUCCESS |
| 1 | FAILURE |
| 2 | UNSTABLE |
| 3 | ABORTED |
| 4 | Unknown result |

### `jkit rebuild --wait`

Same exit codes as `jkit run --wait`.

### Other commands

| Command | Exit 0 | Exit 1 |
|---------|--------|--------|
| `auth status` | Auth valid | Auth invalid |
| `lint` | Jenkinsfile valid | Validation errors |
| All others | Success | Error occurred |

## Non-Interactive Auth

For CI pipelines, use flags and env vars to avoid prompts:

```bash
jkit auth login --host "$JENKINS_URL" --user "$JENKINS_USER" --token "$JENKINS_TOKEN"
```

Isolate config with `JKIT_CONFIG_DIR` to avoid conflicts:

```bash
export JKIT_CONFIG_DIR=$(mktemp -d)
jkit auth login --host "$JENKINS_URL" --user "$JENKINS_USER" --token "$JENKINS_TOKEN"
jkit run my-app --wait
rm -rf "$JKIT_CONFIG_DIR"
```

## Debugging

Use `--verbose` to see HTTP requests and responses:

```bash
jkit status my-app --verbose
# > GET /job/my-app/api/json
# < 200 OK (45ms)
```

Use `--timeout` for slow Jenkins instances:

```bash
jkit log my-app --timeout 60s
```

## Color Behavior

Color is automatically disabled when:
- stdout is not a terminal (piped or redirected)
- `NO_COLOR` env var is set (any value)
- `--no-color` flag is passed

Safe to pipe to files or other tools:

```bash
jkit list > jobs.txt           # no color codes in file
jkit log my-app | grep ERROR   # no color codes in pipe
```

## CI Recipes

### Trigger and Wait

```bash
#!/bin/bash
set -e

jkit auth login --host "$JENKINS_URL" --user "$JENKINS_USER" --token "$JENKINS_TOKEN"

if jkit run my-app -p BRANCH="$GIT_BRANCH" --wait; then
    echo "Build passed"
else
    echo "Build failed (exit $?)"
    exit 1
fi
```

### Check Latest Build Status

```bash
result=$(jkit status my-app --json | jq -r '.[0].result')
if [ "$result" != "SUCCESS" ]; then
    echo "Latest build: $result"
    exit 1
fi
```

### Validate Jenkinsfile in Pre-Commit

```bash
#!/bin/bash
if [ -f Jenkinsfile ]; then
    jkit lint || exit 1
fi
```

### Trigger with Parameters and Stream Log

```bash
jkit run my-app \
    -p BRANCH=main \
    -p ENV=production \
    -p VERSION="$(git describe --tags)" \
    --wait --log
```
