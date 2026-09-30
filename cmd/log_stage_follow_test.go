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
)

// staplerMode is how a Jenkins version answers progressiveText.
type staplerMode int

const (
	// staplerStreaming (Stapler 2050+, Jenkins 2.534+) answers a request that
	// accepts multipart/form-data with a text part and a meta part holding
	// the counted end offset.
	staplerStreaming staplerMode = iota
	// staplerCounted (Stapler before 1979, Jenkins up to 2.508) answers plain
	// text and counts X-Text-Size from what it sent.
	staplerCounted
	// staplerUncounted (Stapler 1979-2049, Jenkins 2.509-2.533) answers plain
	// text with X-Text-Size set to the stored length, ahead of the body while
	// the log is open.
	staplerUncounted
)

var staplerJenkins = map[staplerMode]string{
	staplerStreaming: "2.568.3", staplerCounted: "2.479.3", staplerUncounted: "2.516.3",
}

func (m staplerMode) String() string {
	return map[staplerMode]string{staplerStreaming: "streaming", staplerCounted: "counted", staplerUncounted: "uncounted"}[m]
}

// writeProgressive answers a progressiveText request for a stored log the way
// Stapler in mode does. An open log is sent only up to its last complete
// line, and plain answers at most maxLines lines of it. Plain bodies turn LF
// into CRLF.
func writeProgressive(w http.ResponseWriter, r *http.Request, mode staplerMode, log string, open bool, maxLines int) {
	w.Header().Set("X-Jenkins", staplerJenkins[mode])
	start, _ := strconv.Atoi(r.URL.Query().Get("start"))
	if start > len(log) {
		start = 0
	}
	sent := log[start:]
	if open {
		sent = sent[:strings.LastIndexByte(sent, '\n')+1]
	}
	streaming := mode == staplerStreaming && strings.HasPrefix(r.Header.Get("Accept"), "multipart/form-data")
	if open && !streaming {
		lines := strings.SplitAfter(sent, "\n")
		if len(lines) > maxLines {
			sent = strings.Join(lines[:maxLines], "")
		}
	}
	end := start + len(sent)

	if streaming {
		const b = "8e3512e1-ca67-4429-a54e-bf380e9743df"
		w.Header().Set("Content-Type", "multipart/form-data;boundary="+b+";charset=utf-8")
		meta, _ := json.Marshal(map[string]any{"completed": !open, "start": start, "end": end})
		_, _ = fmt.Fprintf(w, "--%s\r\nContent-Disposition: form-data;name=text\r\nContent-Type: text/plain;charset=utf-8\r\n\r\n%s"+
			"\r\n--%s\r\nContent-Disposition: form-data;name=meta\r\nContent-Type: application/json;charset=utf-8\r\n\r\n%s\r\n--%s--",
			b, sent, b, meta, b)
		return
	}
	size := len(log)
	if mode == staplerCounted {
		size = end
	}
	w.Header().Set("X-Text-Size", strconv.Itoa(size))
	if open {
		w.Header().Set("X-More-Data", "true")
	}
	_, _ = io.WriteString(w, strings.ReplaceAll(sent, "\n", "\r\n"))
}

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
	mu       sync.Mutex
	tick     int
	maxLines int
	starts   map[string][]int64
}

func newStepFollowServer(t *testing.T, mode staplerMode, steps func(tick int) []scriptedStep, stageRunning func(tick int) bool) *stepFollowServer {
	t.Helper()
	s := &stepFollowServer{starts: map[string][]int64{}, maxLines: 10000}
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
			writeProgressive(w, r, mode, st.log, st.active, s.maxLines)
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

// offsetModes are the Stapler answers that say where an open log stops.
var offsetModes = []staplerMode{staplerStreaming, staplerCounted}

func TestLogStageFollowReadsStepsByOffset(t *testing.T) {
	for _, mode := range offsetModes {
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
			assert.Equal(t, []int64{0, 3, 6}, srv.startsFor("10"))
			assert.Equal(t, []int64{0, 3}, srv.startsFor("11"))
		})
	}
}

// TestLogStageFollowKeepsHeldBackText checks the text a server holds back
// from an open log, a partial line or lines past its per-answer line cap, is
// read on a later poll rather than skipped.
func TestLogStageFollowKeepsHeldBackText(t *testing.T) {
	logs := []string{
		"1\n2\n3\n4\n5\npartial-head-",
		"1\n2\n3\n4\n5\npartial-head-partial-tail\n6\n",
		"1\n2\n3\n4\n5\npartial-head-partial-tail\n6\n7\n",
	}
	for _, mode := range offsetModes {
		t.Run(mode.String(), func(t *testing.T) {
			fastStagePolls(t)
			srv := newStepFollowServer(t, mode, func(tick int) []scriptedStep {
				i := min(tick, len(logs)-1)
				return []scriptedStep{{id: "7", log: logs[i], active: tick < 4}}
			}, func(tick int) bool { return tick < 5 })
			srv.maxLines = 2
			setupTestConfig(t, srv.URL)

			out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
			require.NoError(t, err)
			assert.Equal(t, logs[2], out)
		})
	}
}

// TestLogStageFollowUncountedFallsBackToStageLog covers Jenkins 2.509-2.533,
// whose X-Text-Size for an open step log runs past what it sends. With nothing
// printed yet, the whole-stage log is followed instead.
func TestLogStageFollowUncountedFallsBackToStageLog(t *testing.T) {
	fastStagePolls(t)
	logs := []string{"x\ny\npart", "x\ny\npartial li", "x\ny\npartial line\n"}
	srv := newStepFollowServer(t, staplerUncounted, func(tick int) []scriptedStep {
		i := min(tick, len(logs)-1)
		return []scriptedStep{{id: "7", log: logs[i], active: i < len(logs)-1}}
	}, func(tick int) bool { return tick < 2 })
	srv.maxLines = 1
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.NoError(t, err)
	assert.Equal(t, logs[2], out)
	assert.Equal(t, []int64{0}, srv.startsFor("7"), "no step log read after the first answer")
}

// TestLogStageFollowUncountedWaitsForStepEnd covers the same servers once
// earlier steps are printed: the whole-stage log cannot take over, so an open
// step is read only once its log is complete.
func TestLogStageFollowUncountedWaitsForStepEnd(t *testing.T) {
	fastStagePolls(t)
	srv := newStepFollowServer(t, staplerUncounted, func(tick int) []scriptedStep {
		switch tick {
		case 0:
			return []scriptedStep{{id: "10", log: "a\n"}, {id: "11", log: "b1\n", active: true}}
		case 1:
			return []scriptedStep{{id: "10", log: "a\n"}, {id: "11", log: "b1\nb2\nb3", active: true}}
		default:
			return []scriptedStep{{id: "10", log: "a\n"}, {id: "11", log: "b1\nb2\nb3\nb4\n"}}
		}
	}, func(tick int) bool { return tick < 3 })
	srv.maxLines = 1
	setupTestConfig(t, srv.URL)

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f") })
	require.NoError(t, err)
	assert.Equal(t, "a\nb1\nb2\nb3\nb4\n", out)
	assert.Empty(t, stderr)
	assert.Equal(t, []int64{0, 0, 0}, srv.startsFor("11"))
}

func TestLogStageFollowHasNoSizeLimit(t *testing.T) {
	fastStagePolls(t)
	shrinkStageLogCap(t)
	cut := func(n int) string { return bigStageLogBody[:strings.LastIndexByte(bigStageLogBody[:n], '\n')+1] }
	pieces := []string{cut(200), cut(450), bigStageLogBody}
	srv := newStepFollowServer(t, staplerStreaming, func(tick int) []scriptedStep {
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
	assert.Equal(t, []int64{0, int64(len(pieces[0])), int64(len(pieces[1]))}, srv.startsFor("7"))
}

func TestLogStageFollowDrainsStepsAfterStageEnds(t *testing.T) {
	fastStagePolls(t)
	// The listing still reports the step running when the stage has ended.
	srv := newStepFollowServer(t, staplerStreaming, func(tick int) []scriptedStep {
		if tick == 0 {
			return []scriptedStep{{id: "7", log: "building\n", active: true}}
		}
		return []scriptedStep{{id: "7", log: "building\nfinal line\n", active: true}}
	}, func(int) bool { return false })
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.NoError(t, err)
	assert.Equal(t, "building\nfinal line\n", out)
	assert.Equal(t, []int64{0, 9}, srv.startsFor("7"))
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
			writeProgressive(w, r, staplerStreaming, "step log\n", false, 10000)
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
			writeProgressive(w, r, staplerStreaming, log, state == "RUNNING", 10000)
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
// while a step's log stays open on Jenkins 2.509-2.533 after earlier output:
// its end cannot be read safely, which is reported rather than guessed.
func TestLogStageFollowUncountedFinalReadOfOpenStep(t *testing.T) {
	fastStagePolls(t)
	srv := newStepFollowServer(t, staplerUncounted, func(int) []scriptedStep {
		return []scriptedStep{{id: "10", log: "a\n"}, {id: "11", log: "b\n", active: true}}
	}, func(tick int) bool { return tick < 2 })
	setupTestConfig(t, srv.URL)

	out, err := executeCmd(t, "log", "my-app", "5", "--stage-id", "4", "-f")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "step 11 of stage 4 has not closed its log")
	assert.Equal(t, "a\n", out)
}
