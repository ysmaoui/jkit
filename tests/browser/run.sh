#!/usr/bin/env bash
# Renders the viewer test pages with the real renderer and runs each suite in
# Chromium and Firefox inside the Playwright image, with no network.
# Usage: tests/browser/run.sh [suite...]   (default: all; suite = panes, branches, ...)
# Logs and screenshots land in tests/browser/out/.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
# Must match the playwright version in package.json: the image carries the browsers that version drives.
image=mcr.microsoft.com/playwright:v1.50.0-noble
user="$(id -u):$(id -g)"

pages=$(mktemp -d "${TMPDIR:-/tmp}/jkit-browser.XXXXXX")
trap 'rm -rf "$pages"' EXIT
mkdir -p "$here/out/shots"
if ! (cd "$root" && JKIT_BROWSER_PAGES="$pages" go test ./internal/export -run '^TestBrowserPages$' -count=1 >"$here/out/pages.log" 2>&1); then
  echo "rendering test pages failed:" >&2
  cat "$here/out/pages.log" >&2
  exit 1
fi

if ! grep -qs '"version": "1.50.0"' "$here/node_modules/playwright/package.json"; then
  echo "installing node modules into tests/browser/node_modules" >&2
  # The only step that needs the network; proxy settings pass through for corporate networks.
  docker run --rm -u "$user" -e HOME=/tmp \
    -e HTTPS_PROXY -e HTTP_PROXY -e NO_PROXY -e https_proxy -e http_proxy -e no_proxy \
    -v "$here:/suite" -w /suite "$image" npm ci --no-audit --no-fund >&2
fi

all_pages=(golden.html golden-nonode.html golden-missing.html wide-failed.html wide-passed.html)
suites=("$@")
[ $# -gt 0 ] || suites=(panes branches list small wide)
failed=()
for suite in "${suites[@]}"; do
  p=("${all_pages[@]}")
  # The variants differ from golden only in failure fields, which wide does not read.
  [ "$suite" = wide ] && p=(golden.html wide-failed.html wide-passed.html)
  log="$here/out/$suite.log"
  if docker run --rm --network none --ipc=host -u "$user" -e HOME=/tmp -e SHOTS=/out/shots \
    -v "$here:/suite:ro" -v "$here/out:/out" -v "$pages:/pages:ro" -w /pages \
    "$image" node "/suite/$suite.test.js" "${p[@]}" >"$log" 2>&1; then
    echo "$suite: $(grep -c '^PASS' "$log") passed"
  else
    echo "$suite: FAILED ($(grep -c '^FAIL' "$log") checks), see tests/browser/out/$suite.log"
    grep '^FAIL' "$log" | head -20 || tail -20 "$log"
    failed+=("$suite")
  fi
done
[ ${#failed[@]} -eq 0 ] || { echo "failed suites: ${failed[*]}" >&2; exit 1; }
