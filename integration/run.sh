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

PASS=0
FAIL=0
ERRORS=""

cleanup() {
    rm -rf "$JKIT_CONFIG_DIR"
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
        FAIL=$((FAIL + 1))
        echo "  FAIL  $desc"
        ERRORS="${ERRORS}  - ${desc}: exit $?\n    ${output}\n"
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
assert_contains "status test-pipeline 1 shows Build stage" "Build" \
    "$JK" status test-pipeline 1

echo ""
echo "Done."
