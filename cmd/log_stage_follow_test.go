package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/staplertest"
)

// scriptedStep is one step of stage 4 as of poll tick: its log so far, whether
// it still runs, and whether it failed. A step with log "" has no log action,
// so its log route 404s.
type scriptedStep struct {
	id     string
	log    string
	active bool
	failed bool
}

// stepFollowServer serves stage 4 of build 5 through PGV with step logs
// answered as Stapler in mode does. tick is the number of stages/tree requests
// so far, one per follow poll. steps gives the step listing at a tick,
// stageRunning the stage state. stages/log concatenates the steps the way PGV
// does.
type stepFollowServer struct {
	*httptest.Server
	mu     sync.Mutex
	tick   int
	starts map[string][]int64
}

func newStepFollowServer(t *testing.T, mode staplertest.Mode, steps func(tick int) []scriptedStep, stageRunning func(tick int) bool) *stepFollowServer {
	t.Helper()
	s := &stepFollowServer{starts: map[string][]int64{}}
	find := func(id string) (scriptedStep, bool) {
		for _, st := range steps(s.tick) {
			if st.id == id {
				return st, true
			}
		}
		return scriptedStep{}, false
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		p := r.URL.Path
		switch {
		case p == "/api/json":
			staplertest.WriteRoot(w, mode)
		case strings.HasSuffix(p, "/5/stages/tree"):
			s.tick++
			state := "success"
			if stageRunning(s.tick) {
				state = "running"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{
				"stages": []map[string]any{{"id": "4", "name": "Build", "type": "STAGE", "state": state}},
			}})
		case strings.HasSuffix(p, "/5/stages/steps"):
			var list []map[string]any
			for _, st := range steps(s.tick) {
				state := "success"
				switch {
				case st.active:
					state = "running"
				case st.failed:
					state = "failure"
				}
				list = append(list, map[string]any{"id": st.id, "name": "sh", "state": state, "stageId": "4"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{"steps": list}})
		case strings.HasSuffix(p, "/5/stages/log"):
			for _, st := range steps(s.tick) {
				_, _ = io.WriteString(w, st.log)
				if st.failed {
					_, _ = io.WriteString(w, "boom\n")
				}
			}
		case strings.HasSuffix(p, "/5/stages/exceptionText"):
			if st, ok := find(r.URL.Query().Get("nodeId")); ok && st.failed {
				_, _ = fmt.Fprint(w, "boom")
			}
		case strings.HasSuffix(p, "/log/logText/progressiveText"):
			id := strings.Split(strings.TrimPrefix(p, "/job/my-app/5/execution/node/"), "/")[0]
			st, ok := find(id)
			if !ok || st.log == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			start, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
			s.starts[id] = append(s.starts[id], start)
			staplertest.WriteProgressive(w, r, mode, st.log, st.active)
		case strings.HasPrefix(p, "/job/my-app/5/execution/node/"):
			_, _ = fmt.Fprint(w, "<html>flow node</html>")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stepFollowServer) startsFor(id string) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts[id]
}

func fastStagePolls(t *testing.T) {
	t.Helper()
	old := stagePollInterval
	stagePollInterval = time.Millisecond
	t.Cleanup(func() { stagePollInterval = old })
}

func TestLogStageFollowReadsStepsByOffset(t *testing.T) {
	// Only a counted answer says where an open log's text stops. A multipart
	// server is asked for the last LF before the stored length it reported:
	// at the byte before it, then for the unterminated "b2" where the text
	// written puts it, offset 2, with none at 3. A plain one is asked from
	// one byte before the stored length, and when that byte is not a
	// newline, from the last known line start.
	wantStarts := map[staplertest.Mode]map[string][]int64{
		staplertest.Streaming: {"10": {0, 2, 5}, "11": {0, 4, 2, 3}},
		staplertest.Counted:   {"10": {0, 3, 6}, "11": {0, 3}},
		staplertest.Uncounted: {"10": {0, 2, 5}, "11": {0, 4, 0}},
		// Overrun also checks after each answer that the log did not grow.
		staplertest.Overrun: {"10": {0, 3, 2, 6, 5}, "11": {0, 5, 4, 0}},
	}
	for _, mode := range staplertest.Modes {
		t.Run(mode.String(), func(t *testing.T) {
			fastStagePolls(t)
			srv := newStepFollowServer(t, mode, func(tick int) []scriptedStep {
				switch tick {
				case 0:
					return []scriptedStep{{id: "10", log: "a1\n", active: true}}
				case 1:
					return []scriptedStep{
						{id: "10", log: "a1\na2\n", active: true},
						{id: "11", log: "b1\n", active: true},
					}
				case 2:
					return []scriptedStep{
						{id: "10", log: "a1\na2\na3\n"},
						{id: "11", log: "b1\nb2", active: true},
					}
				default:
					return []scriptedStep{
						{id: "10", log: "a1\na2\na3\n"},
						{id: "11", log: "b1\nb2\nb3\n"},
						{id: "12", failed: true},
					}
				}
			}, func(tick int) bool { return tick < 4 })
			setupTestConfig(t, srv.URL)

			var out string
			var err error
			stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f") })
			require.NoError(t, err)
			assert.Equal(t, "a1\na2\na3\nb1\nb2\nb3\nboom\n", out)
			assert.Empty(t, stderr)
			// A step that is finished and drained is never fetched again, a
			// later step waits until the one before it finishes, and the
			// partial "b2" held back at offset 3 is read with the rest.
			assert.Equal(t, wantStarts[mode]["10"], srv.startsFor("10"))
			assert.Equal(t, wantStarts[mode]["11"], srv.startsFor("11"))
		})
	}
}

// TestLogStageFollowKeepsHeldBackText checks the text a server holds back
// from an open log, a partial line or lines past its per-answer line cap, is
// read on a later poll rather than skipped.
func TestLogStageFollowKeepsHeldBackText(t *testing.T) {
	head := numberedLines(staplertest.MaxLinesRead + 5)
	logs := []string{
		head + "partial-head-",
		head + "partial-head-partial-tail\n6\n",
		head + "partial-head-partial-tail\n6\n7\n",
	}
	for _, mode := range staplertest.Modes {
		t.Run(mode.String(), func(t *testing.T) {
			fastStagePolls(t)
			srv := newStepFollowServer(t, mode, func(tick int) []scriptedStep {
				i := min(tick, len(logs)-1)
				return []scriptedStep{{id: "7", log: logs[i], active: tick < 4}}
			}, func(tick int) bool { return tick < 5 })
			setupTestConfig(t, srv.URL)

			out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
			require.NoError(t, err)
			assert.Equal(t, logs[2], out)
		})
	}
}

// TestLogStageFollowUncountedWaitsForStepEnd covers Jenkins 2.509-2.533 once
// a running step writes more lines than one plain answer holds: the answer
// does not say where it stopped, so the rest shows once the step completes,
// and meanwhile only the step's state is checked.
func TestLogStageFollowUncountedWaitsForStepEnd(t *testing.T) {
	fastStagePolls(t)
	burst := numberedLines(staplertest.MaxLinesRead + 3)
	srv := newStepFollowServer(t, staplertest.Uncounted, func(tick int) []scriptedStep {
		switch tick {
		case 0:
			return []scriptedStep{{id: "10", log: "a\n"}, {id: "11", log: "b1\n", active: true}}
		case 1, 2:
			return []scriptedStep{{id: "10", log: "a\n"}, {id: "11", log: "b1\n" + burst, active: true}}
		default:
			return []scriptedStep{{id: "10", log: "a\n"}, {id: "11", log: "b1\n" + burst + "b4\n"}}
		}
	}, func(tick int) bool { return tick < 4 })
	setupTestConfig(t, srv.URL)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f") })
	require.NoError(t, err)
	assert.Equal(t, "a\nb1\n"+burst+"b4\n", out)
	assert.Empty(t, stderr)
	// From the line start after "b1", a capped answer, a state check, then
	// the rest once complete.
	assert.Equal(t, []int64{0, 2, 3, 3, 3}, srv.startsFor("11"))
}

func TestLogStageFollowHasNoSizeLimit(t *testing.T) {
	fastStagePolls(t)
	shrinkStageLogCap(t)
	cut := func(n int) string { return bigStageLogBody[:strings.LastIndexByte(bigStageLogBody[:n], '\n')+1] }
	pieces := []string{cut(200), cut(450), bigStageLogBody}
	srv := newStepFollowServer(t, staplertest.Streaming, func(tick int) []scriptedStep {
		i := min(tick, len(pieces)-1)
		return []scriptedStep{{id: "7", log: pieces[i], active: i < len(pieces)-1}}
	}, func(tick int) bool { return tick < len(pieces) })
	setupTestConfig(t, srv.URL)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f") })
	require.NoError(t, err)
	assert.Equal(t, bigStageLogBody, out)
	assert.NotContains(t, stderr, "stopped following")
	assert.Equal(t, []int64{0, int64(len(pieces[0])) - 1, int64(len(pieces[1])) - 1}, srv.startsFor("7"))
}

func TestLogStageFollowDrainsStepsAfterStageEnds(t *testing.T) {
	fastStagePolls(t)
	// The listing still reports the step running when the stage has ended.
	srv := newStepFollowServer(t, staplertest.Streaming, func(tick int) []scriptedStep {
		if tick == 0 {
			return []scriptedStep{{id: "7", log: "building\n", active: true}}
		}
		return []scriptedStep{{id: "7", log: "building\nfinal line\n", active: true}}
	}, func(int) bool { return false })
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.NoError(t, err)
	assert.Equal(t, "building\nfinal line\n", out)
	assert.Equal(t, []int64{0, 8}, srv.startsFor("7"))
}

// TestLogStageFollowRetriesStepListing checks one failed step listing does
// not hand the follow to the capped whole-stage log.
func TestLogStageFollowRetriesStepListing(t *testing.T) {
	fastStagePolls(t)
	var mu sync.Mutex
	var listCalls, treeCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch p := r.URL.Path; {
		case strings.HasSuffix(p, "/stages/tree"):
			treeCalls++
			state := "running"
			if treeCalls > 2 {
				state = "success"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{
				"stages": []map[string]any{{"id": "4", "name": "Build", "type": "STAGE", "state": state}},
			}})
		case strings.HasSuffix(p, "/stages/steps"):
			listCalls++
			if listCalls == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{
				"steps": []map[string]any{{"id": "7", "state": "success"}},
			}})
		case strings.HasSuffix(p, "/log/logText/progressiveText"):
			staplertest.WriteProgressive(w, r, staplertest.Streaming, "step log\n", false)
		case strings.HasSuffix(p, "/stages/log"):
			_, _ = fmt.Fprint(w, "whole stage log\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.NoError(t, err)
	assert.Equal(t, "step log\n", out)
}

// TestLogStageFollowFallsBackWithoutStepRoute covers a server that lists steps
// but serves neither step logs nor flow node pages: the whole-stage log is
// followed instead.
func TestLogStageFollowFallsBackWithoutStepRoute(t *testing.T) {
	fastStagePolls(t)
	var stageLogCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case strings.HasSuffix(p, "/stages/tree"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{
				"stages": []map[string]any{{"id": "4", "name": "Build", "type": "STAGE", "state": "success"}},
			}})
		case strings.HasSuffix(p, "/stages/steps"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{
				"steps": []map[string]any{{"id": "7", "state": "success"}},
			}})
		case strings.Contains(p, "/stages/log"):
			stageLogCalls++
			_, _ = fmt.Fprint(w, "whole stage log\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.NoError(t, err)
	assert.Equal(t, "whole stage log\n", out)
	assert.Equal(t, 2, stageLogCalls, "one poll plus the final read")
}

func TestStreamStageLogReturnsOnCancel(t *testing.T) {
	stall := func(w http.ResponseWriter, release chan struct{}) {
		w.(http.Flusher).Flush()
		<-release
	}
	cases := map[string]func(w http.ResponseWriter, release chan struct{}){
		"mid step body": func(w http.ResponseWriter, release chan struct{}) {
			w.Header().Set("Content-Type", "multipart/form-data;boundary=b")
			_, _ = fmt.Fprint(w, "--b\r\nContent-Disposition: form-data;name=text\r\n\r\npartial\n")
			stall(w, release)
		},
		"before step headers": func(w http.ResponseWriter, release chan struct{}) {
			<-release
		},
	}
	for name, stepLog := range cases {
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch p := r.URL.Path; {
				case strings.HasSuffix(p, "/stages/steps"):
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{
						"steps": []map[string]any{{"id": "7", "state": "running"}},
					}})
				case strings.HasSuffix(p, "/log/logText/progressiveText"):
					stepLog(w, release)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			defer close(release)

			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(100*time.Millisecond, cancel)
			done := make(chan error, 1)
			go func() {
				done <- streamStageLog(ctx, api.NewClient(srv.URL, "u", "t"), "my-app", 5, "4", io.Discard, io.Discard)
			}()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("streamStageLog did not return after cancel")
			}
		})
	}
}

// TestLogStageFollowBlueOceanStepListing follows a running stage whose steps
// only Blue Ocean lists, as on a server where Blue Ocean's whole-stage log
// fails with 500 and PGV is absent.
func TestLogStageFollowBlueOceanStepListing(t *testing.T) {
	fastStagePolls(t)
	var mu sync.Mutex
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		log, state := "one\n", "RUNNING"
		if polls > 0 {
			log, state = "one\ntwo\n", "FINISHED"
		}
		switch p := r.URL.Path; {
		case strings.HasSuffix(p, "/runs/5/nodes/"):
			polls++
			result := "UNKNOWN"
			if polls > 1 {
				result = "SUCCESS"
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "4", "displayName": "Build", "result": result}})
		case strings.HasSuffix(p, "/runs/5/nodes/4/steps/"):
			if r.URL.Query().Get("start") != "0" {
				_, _ = fmt.Fprint(w, "[]")
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "7", "state": state}})
		case p == "/job/my-app/5/execution/node/7/log/logText/progressiveText":
			staplertest.WriteProgressive(w, r, staplertest.Streaming, log, state == "RUNNING")
		case strings.HasSuffix(p, "/5/api/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "building": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.NoError(t, err)
	assert.Equal(t, "one\ntwo\n", out)
}

// TestLogStageFollowFallsBackOnStepFailure checks that a step-by-step failure
// before any output leaves the stage to the whole-stage log, which may still
// serve it.
func TestLogStageFollowFallsBackOnStepFailure(t *testing.T) {
	okListing := `{"status":"ok","data":{"steps":[{"id":"7","state":"success"}]}}`
	cases := map[string]struct {
		listing string
		stepLog func(w http.ResponseWriter)
	}{
		"step log forbidden": {okListing, func(w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) }},
		"step log is a page": {okListing, func(w http.ResponseWriter) { _, _ = fmt.Fprint(w, "<html>login</html>") }},
		"listing not ok":     {`{"status":"error","data":{}}`, nil},
		"listing not JSON":   {`<html>proxy error</html>`, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fastStagePolls(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch p := r.URL.Path; {
				case strings.HasSuffix(p, "/stages/tree"):
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{
						"stages": []map[string]any{{"id": "4", "name": "Build", "type": "STAGE", "state": "success"}},
					}})
				case strings.HasSuffix(p, "/stages/steps"):
					_, _ = fmt.Fprint(w, tc.listing)
				case strings.HasSuffix(p, "/log/logText/progressiveText") && tc.stepLog != nil:
					tc.stepLog(w)
				case strings.HasSuffix(p, "/stages/log"):
					_, _ = fmt.Fprint(w, "whole stage log\n")
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			setupTestConfig(t, srv.URL)

			out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
			require.NoError(t, err)
			assert.Equal(t, "whole stage log\n", out)
		})
	}
}

// TestLogStageFollowGivesUpOnStepListing checks a step listing that keeps
// failing hands the follow to the whole-stage log after maxStepListFailures
// polls, and is not asked again.
func TestLogStageFollowGivesUpOnStepListing(t *testing.T) {
	fastStagePolls(t)
	var mu sync.Mutex
	var listCalls, treeCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch p := r.URL.Path; {
		case strings.HasSuffix(p, "/stages/tree"):
			treeCalls++
			state := "running"
			if treeCalls > 5 {
				state = "success"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{
				"stages": []map[string]any{{"id": "4", "name": "Build", "type": "STAGE", "state": state}},
			}})
		case strings.HasSuffix(p, "/stages/steps"):
			listCalls++
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasSuffix(p, "/stages/log"):
			_, _ = fmt.Fprint(w, "whole stage log\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.NoError(t, err)
	assert.Equal(t, "whole stage log\n", out)
	assert.Equal(t, 3, listCalls)
}

// TestLogStageFollowUncountedFinalReadOfOpenStep covers a stage that ends
// while a step's log stays open past the line cap on Jenkins 2.509-2.533
// after earlier output: its end cannot be read safely, which is reported
// rather than guessed.
func TestLogStageFollowUncountedFinalReadOfOpenStep(t *testing.T) {
	fastStagePolls(t)
	burst := numberedLines(staplertest.MaxLinesRead + 1)
	srv := newStepFollowServer(t, staplertest.Uncounted, func(int) []scriptedStep {
		return []scriptedStep{{id: "10", log: "a\n"}, {id: "11", log: burst, active: true}}
	}, func(tick int) bool { return tick < 2 })
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "step 11 of stage 4 has not closed its log")
	assert.Equal(t, "a\n"+numberedLines(staplertest.MaxLinesRead), out)
}

// numberedLines is n distinct lines.
func numberedLines(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "line %05d\n", i)
	}
	return b.String()
}
