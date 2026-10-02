// Package staplertest answers progressiveText requests the way each Stapler
// generation behind Jenkins does, for tests of code that follows a log.
package staplertest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Mode is how a Jenkins version answers progressiveText.
type Mode int

const (
	// Streaming (Stapler 2050+, Jenkins 2.534+) answers a request that
	// accepts multipart/form-data with a text part and a meta part.
	Streaming Mode = iota
	// Counted (Stapler before 1979, Jenkins up to 2.508) answers plain text
	// and counts X-Text-Size from what it sent.
	Counted
	// Uncounted (Stapler 2029-2049, Jenkins 2.527-2.533) answers plain
	// text with X-Text-Size set to the stored length, ahead of the body while
	// the log is open.
	Uncounted
	// Overrun (Stapler 1979-2028, Jenkins 2.509-2.526) answers as Uncounted,
	// but reads on while the log grows during the answer, so the body can
	// run past X-Text-Size.
	Overrun
)

// MaxLinesRead is Stapler's LargeText.MAX_LINES_READ.
const MaxLinesRead = 10000

// Modes lists every Mode.
var Modes = []Mode{Streaming, Counted, Uncounted, Overrun}

// Jenkins is a version whose Stapler answers as the mode does.
func (m Mode) Jenkins() string {
	return map[Mode]string{Streaming: "2.568.3", Counted: "2.479.3", Uncounted: "2.528.3", Overrun: "2.509"}[m]
}

func (m Mode) String() string {
	return map[Mode]string{Streaming: "streaming", Counted: "counted", Uncounted: "uncounted", Overrun: "overrun"}[m]
}

var noteRe = regexp.MustCompile(`\x1b\[8mha:.*?\x1b\[0m`)

// WriteProgressive answers a progressiveText request for a stored log, as
// Jenkins 2.479.3 and 2.568.3 were seen to and Stapler's LargeText reads:
//
//   - A start past the log's end is answered from 0.
//   - A streaming answer strips console notes, and for an open log stops at
//     its last LF (core's PlainTextConsoleOutputStream). Its meta "end" is the
//     stored length, an unterminated last line included. A negative start
//     asks for the tail: from 0 when the log is no longer, else from the
//     first line start in the tail, or where it lands when there is none.
//     searchNewLineUntil=U moves a start to after the first LF before U-1.
//   - A plain answer keeps notes and turns every LF not after a CR into CRLF.
//     For an open log it stops at the last CR or LF and after MaxLinesRead of
//     them (Stapler's TailMark). A negative start gets the headers and no text.
//   - Bytes that are not valid UTF-8 are sent as U+FFFD, one per byte.
//   - X-Jenkins is not set: Jenkins sends it on api/json, not here. See
//     WriteRoot.
func WriteProgressive(w http.ResponseWriter, r *http.Request, mode Mode, log string, open bool) {
	WriteProgressiveDuring(w, r, mode, log, log, open)
}

// WriteProgressiveDuring is WriteProgressive for a log that is log when the
// request arrives and has grown to grown by the time the body is written.
// Overrun sends the growth. Uncounted reads it but stops after the stored
// length log had, which may cut a line or a character. The others size and
// send what log holds.
func WriteProgressiveDuring(w http.ResponseWriter, r *http.Request, mode Mode, log, grown string, open bool) {
	start, _ := strconv.Atoi(r.URL.Query().Get("start"))
	streaming := mode == Streaming && strings.HasPrefix(r.Header.Get("Accept"), "multipart/form-data")
	if start > len(log) {
		start = 0
	}
	if start < 0 && !streaming {
		w.Header().Set("Content-Type", "text/plain;charset=utf-8")
		w.Header().Set("X-Text-Size", strconv.Itoa(len(log)))
		if open {
			w.Header().Set("X-More-Data", "true")
		}
		return
	}
	meta := map[string]any{"completed": !open}
	if start < 0 {
		from := len(log) + start
		start = max(from, 0)
		if from > 0 {
			if i := strings.IndexByte(log[from:len(log)-1], '\n'); i >= 0 {
				start = from + i + 1
				meta["startFromNewLine"] = true
			}
		}
	}
	if until := r.URL.Query().Get("searchNewLineUntil"); streaming && until != "" && start >= 0 {
		stop, _ := strconv.Atoi(until)
		for p := start; p+1 < stop && p < len(log); p++ {
			if log[p] == '\n' {
				start = p + 1
				meta["startFromNewLine"] = true
				break
			}
		}
	}
	sent := log[start:]
	if streaming {
		if open {
			sent = sent[:strings.LastIndexByte(sent, '\n')+1]
		}
		const b = "8e3512e1-ca67-4429-a54e-bf380e9743df"
		meta["start"], meta["end"] = start, len(log)
		m, _ := json.Marshal(meta)
		w.Header().Set("Content-Type", "multipart/form-data;boundary="+b+";charset=utf-8")
		_, _ = fmt.Fprintf(w, "--%s\r\nContent-Disposition: form-data;name=text\r\nContent-Type: text/plain;charset=utf-8\r\n\r\n%s"+
			"\r\n--%s\r\nContent-Disposition: form-data;name=meta\r\nContent-Type: application/json;charset=utf-8\r\n\r\n%s\r\n--%s--",
			b, Decode(noteRe.ReplaceAllString(sent, "")), b, m, b)
		return
	}
	if mode == Overrun || mode == Uncounted {
		sent = grown[start:]
	}
	if open {
		ends := 0
		cut := 0
		for i := 0; i < len(sent) && ends < MaxLinesRead; i++ {
			if sent[i] == '\r' || sent[i] == '\n' {
				ends++
				cut = i + 1
			}
		}
		sent = sent[:cut]
		// From Stapler 2029 (#703) a ThresholdingOutputStream stops the body
		// after the stored length the answer reported, wherever that falls.
		if mode == Uncounted && len(sent) > len(log)-start {
			sent = sent[:len(log)-start]
		}
	}
	size := len(log)
	if mode == Counted && open {
		size = start + len(sent)
	}
	w.Header().Set("Content-Type", "text/plain;charset=utf-8")
	w.Header().Set("X-Text-Size", strconv.Itoa(size))
	if open {
		w.Header().Set("X-More-Data", "true")
	}
	_, _ = io.WriteString(w, crlf(Decode(sent)))
}

// Decode is what Jenkins makes of stored bytes it sends as text: each byte
// that is not valid UTF-8 becomes U+FFFD.
func Decode(s string) string {
	var b strings.Builder
	for len(s) > 0 {
		r, n := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && n == 1 {
			b.WriteRune(utf8.RuneError)
		} else {
			b.WriteString(s[:n])
		}
		s = s[n:]
	}
	return b.String()
}

// crlf is Stapler's LineEndNormalizingWriter.
func crlf(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' && (i == 0 || s[i-1] != '\r') {
			b.WriteByte('\r')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// WriteRoot answers GET /api/json with the mode's X-Jenkins, which is where
// the client learns the version.
func WriteRoot(w http.ResponseWriter, mode Mode) {
	w.Header().Set("X-Jenkins", mode.Jenkins())
	_, _ = io.WriteString(w, `{"_class":"hudson.model.Hudson"}`)
}

// ConsoleText answers consoleText: the stored log with notes stripped, as it
// stands.
func ConsoleText(w http.ResponseWriter, log string) {
	w.Header().Set("Content-Type", "text/plain;charset=utf-8")
	_, _ = io.WriteString(w, noteRe.ReplaceAllString(log, ""))
}
