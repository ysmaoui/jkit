#!/usr/bin/env bash
set -euo pipefail

JENKINS_URL=http://localhost:${JENKINS_PORT:-9090}
JENKINS_USER=admin
JENKINS_TOKEN=admin

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
PROJECT_DIR=$(cd "$SCRIPT_DIR/.." && pwd)
JK="$PROJECT_DIR/jkit"

export JKIT_CONFIG_DIR=$(mktemp -d)
export NO_COLOR=1
WORK_DIR=$(mktemp -d)

PASS=0
FAIL=0
ERRORS=""

cleanup() {
    rm -rf "$JKIT_CONFIG_DIR" "$WORK_DIR"
    echo ""
    echo "================================"
    echo "Results: $PASS passed, $FAIL failed"
    echo "================================"
    if [ -n "$ERRORS" ]; then
        echo ""
        echo "Failures:"
        echo "$ERRORS"
    fi
    if [ "$FAIL" -gt 0 ]; then
        exit 1
    fi
}
trap cleanup EXIT

assert_success() {
    local desc="$1"; shift
    if output=$("$@" 2>&1); then
        PASS=$((PASS + 1))
        echo "  PASS  $desc"
    else
        local rc=$?
        FAIL=$((FAIL + 1))
        echo "  FAIL  $desc"
        ERRORS="${ERRORS}  - ${desc}: exit ${rc}\n    ${output}\n"
    fi
}

assert_fail() {
    local desc="$1"; shift
    if output=$("$@" 2>&1); then
        FAIL=$((FAIL + 1))
        echo "  FAIL  $desc (expected failure, got success)"
        ERRORS="${ERRORS}  - ${desc}: expected failure\n    ${output}\n"
    else
        PASS=$((PASS + 1))
        echo "  PASS  $desc"
    fi
}

assert_contains() {
    local desc="$1"
    local expected="$2"
    shift 2
    if output=$("$@" 2>&1); then
        if echo "$output" | grep -qi "$expected"; then
            PASS=$((PASS + 1))
            echo "  PASS  $desc"
        else
            FAIL=$((FAIL + 1))
            echo "  FAIL  $desc (output missing '$expected')"
            ERRORS="${ERRORS}  - ${desc}: missing '${expected}'\n    ${output}\n"
        fi
    else
        local rc=$?
        FAIL=$((FAIL + 1))
        echo "  FAIL  $desc (exit $rc)"
        ERRORS="${ERRORS}  - ${desc}: exit ${rc}\n    ${output}\n"
    fi
}

# Wait for Jenkins
echo "Waiting for Jenkins..."
for i in $(seq 1 60); do
    if curl -fs "$JENKINS_URL/login" > /dev/null 2>&1; then
        echo "Jenkins is up (attempt $i)"
        break
    fi
    if [ "$i" -eq 60 ]; then
        echo "Jenkins failed to start after 60 attempts"
        exit 1
    fi
    sleep 5
done

# Extra wait for init scripts to complete
echo "Waiting for init scripts..."
sleep 10

# Build binary
echo "Building jkit..."
(cd "$PROJECT_DIR" && go build -o jkit .)

# export-pipeline runs jkit inside the controller. The chmod undoes a
# restrictive host umask: the container's jenkins user owns nothing copied in.
echo "Copying a linux jkit into the Jenkins container..."
(cd "$PROJECT_DIR" && GOOS=linux CGO_ENABLED=0 go build -o "$WORK_DIR/jkit" .)
chmod 755 "$WORK_DIR/jkit"
docker compose -f "$SCRIPT_DIR/docker-compose.yml" cp "$WORK_DIR/jkit" jenkins:/usr/local/bin/jkit

echo ""
echo "Running integration tests..."
echo ""

# 1. Auth
echo "[auth]"
assert_success "auth login" \
    "$JK" auth login --host "$JENKINS_URL" --user "$JENKINS_USER" --token "$JENKINS_TOKEN"

assert_contains "auth status shows valid" "valid" \
    "$JK" auth status

# 2. List jobs
echo ""
echo "[list]"
assert_contains "list shows test-job" "test-job" \
    "$JK" list
assert_contains "list shows test-pipeline" "test-pipeline" \
    "$JK" list
assert_contains "list shows test-folder" "test-folder" \
    "$JK" list
assert_contains "list shows param-job" "param-job" \
    "$JK" list

assert_contains "list --folder test-folder shows inner-job" "inner-job" \
    "$JK" list --folder test-folder

# 3. Run jobs
echo ""
echo "[run]"
assert_contains "run test-job" "queued" \
    "$JK" run test-job

sleep 3

assert_success "run test-job --wait" \
    "$JK" run test-job --wait

assert_contains "run param-job with params" "queued" \
    "$JK" run param-job -p BRANCH=feature -p ENV=staging

assert_contains "run param-job without params" "queued" \
    "$JK" run param-job

assert_success "run test-pipeline --wait" \
    "$JK" run test-pipeline --wait

# 4. Status
echo ""
echo "[status]"
assert_contains "status test-job shows SUCCESS" "SUCCESS" \
    "$JK" status test-job

assert_contains "status test-job 1 shows #1" "#1" \
    "$JK" status test-job 1

# 5. Log
echo ""
echo "[log]"
assert_contains "log test-job 1 shows output" "Hello from test-job" \
    "$JK" log test-job 1

# 6. Lint
echo ""
echo "[lint]"
assert_contains "lint valid Jenkinsfile" "successfully validated" \
    "$JK" lint "$SCRIPT_DIR/Jenkinsfile.valid"

assert_fail "lint invalid Jenkinsfile" \
    "$JK" lint "$SCRIPT_DIR/Jenkinsfile.invalid"

# 7. JSON output
echo ""
echo "[json]"
assert_contains "list --json has name field" '"name"' \
    "$JK" list --json

# 8. Pipeline stages
echo ""
echo "[stages]"
assert_contains "status test-pipeline 1 shows stages" "Stages:" \
    "$JK" status test-pipeline 1

assert_contains "stages test-pipeline 1 lists Deploy stage" "Deploy *STAGE *SUCCESS" \
    "$JK" stages test-pipeline 1

assert_success "wait on finished test-pipeline Build stage" \
    "$JK" wait test-pipeline 1 --stage Build --max-wait 1m

# 9. Export. export-pipeline exports itself mid-run and archives the result.
echo ""
echo "[export]"
assert_success "run export-pipeline --wait (exports itself mid-run)" \
    "$JK" run export-pipeline --wait

assert_success "export html export-pipeline 1" \
    "$JK" export html export-pipeline 1 -o "$WORK_DIR/export.html"

assert_success "export file is non-empty" \
    test -s "$WORK_DIR/export.html"

assert_success "fetch PGV allSteps of export-pipeline 1" \
    curl -fsS -u "$JENKINS_USER:$JENKINS_TOKEN" -o "$WORK_DIR/allSteps.json" \
    "$JENKINS_URL/job/export-pipeline/1/stages/allSteps"

assert_success "finished export holds every stage, step and log" \
    go -C "$PROJECT_DIR" run ./integration/exportcheck finished "$WORK_DIR/export.html" "$WORK_DIR/allSteps.json"

assert_success "run console-pipeline --wait" \
    "$JK" run console-pipeline --wait

# Both log sources give the same page. The console export must not have
# fallen back to steps, or the comparison proves nothing.
for job in export-pipeline console-pipeline; do
    assert_success "export html $job 1 from the annotated console, no fallback" \
        sh -c '"$1" export html "$2" 1 --log-source=console -o "$3" 2>"$3.err" && grep -q "reading step logs from the annotated console" "$3.err" && ! grep -q unusable "$3.err"' \
        _ "$JK" "$job" "$WORK_DIR/$job-console.html"
    assert_success "export html $job 1 step by step" \
        "$JK" export html "$job" 1 --log-source=steps -o "$WORK_DIR/$job-steps.html"
    assert_success "$job: same step logs and console from both log sources" \
        go -C "$PROJECT_DIR" run ./integration/exportcheck same "$WORK_DIR/$job-console.html" "$WORK_DIR/$job-steps.html"
done

assert_fail "run compile-error-pipeline --wait" \
    "$JK" run compile-error-pipeline --wait

assert_success "export html compile-error-pipeline 1: finished, compile error as build error" \
    sh -c '"$1" export html compile-error-pipeline 1 -o "$2" 2>"$2.err" && ! grep -q "still in progress" "$2.err" && grep -qF "$3" "$2"' \
    _ "$JK" "$WORK_DIR/compile-error.html" \
    "\"buildErrorMessage\":\"org.codehaus.groovy.control.MultipleCompilationErrorsException: startup failed: WorkflowScript: 7: expecting '}', found '' @ line 7, column 1.\""

assert_success "download in-pipeline snapshot artifact" \
    "$JK" artifacts export-pipeline 1 -d snapshot.html -o "$WORK_DIR/snapshot.html"

assert_success "in-pipeline snapshot is partial, exporting step running" \
    go -C "$PROJECT_DIR" run ./integration/exportcheck live "$WORK_DIR/snapshot.html"

assert_success "download in-pipeline step-by-step snapshot and its stderr" \
    sh -c '"$1" artifacts export-pipeline 1 -d snapshot-steps.html -o "$2/snapshot-steps.html" && "$1" artifacts export-pipeline 1 -d snapshot.err -o "$2/snapshot.err"' \
    _ "$JK" "$WORK_DIR"

# The console stops inside Long Log: Build's three steps and Race's sh and
# two echos come from it; Race's sleeps and the steps from Long Log on are
# read one by one.
assert_success "in-pipeline snapshot took logs from the annotated console and read the rest one by one" \
    sh -c 'grep -q "build running: 6 step logs whole in the annotated console, reading the other 8 one by one" "$1" && ! grep -q unusable "$1"' \
    _ "$WORK_DIR/snapshot.err"

assert_success "in-pipeline snapshot: finished steps match the step-by-step snapshot" \
    go -C "$PROJECT_DIR" run ./integration/exportcheck ended "$WORK_DIR/snapshot.html" "$WORK_DIR/snapshot-steps.html"

echo ""
echo "Done."
