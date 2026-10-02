package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// maxLinesRead is Stapler's LargeText.MAX_LINES_READ: a plain answer for a log
// that is still being written stops after this many line ends, CR or LF.
const maxLinesRead = 10000

// errLogRestarted means the server answered from an earlier offset than asked,
// which Stapler does when the stored log is shorter than the offset: it
// resends the log from 0.
var errLogRestarted = errors.New("server restarted the log from an earlier offset")

// errNotLineStart stops copying an answer whose text does not begin with the
// newline that ends the line before the offset asked for.
var errNotLineStart = errors.New("offset is not a line start")

// progressiveAnswer is an open progressiveText answer whose headers are read
// and whose text is not.
//
// Where its text ends depends on what serves it:
//
//   - multipart (Stapler 2050, Jenkins 2.534): the text part is written through
//     core's PlainTextConsoleOutputStream, which strips console notes and,
//     while the log is written, holds back the unterminated last line. The
//     meta part's "end" is the stored length, that line included, so it is
//     exact only once the log is complete.
//   - plain, log complete: every Stapler sends the rest of the log and sets
//     X-Text-Size to its end.
//   - plain, log written, Jenkins up to 2.508 (Stapler before 1979):
//     X-Text-Size counts what was sent, which stops at the last CR or LF and
//     after maxLinesRead of them.
//   - plain, log written, any later Jenkins: X-Text-Size is the stored length.
//     The body stops at the last CR or LF and after maxLinesRead of them, as
//     the log stands while it is read. Stapler 1979 to 2028 could so send a
//     body running past X-Text-Size; 2029 (#703) and later cut the body after
//     X-Text-Size bytes, which is mid-line, even mid-character, when the log
//     grew during the answer.
//
// Plain bodies keep console notes and go through Stapler's
// LineEndNormalizingWriter, which turns a lone LF into CRLF.
type progressiveAnswer struct {
	resp      *http.Response
	start     int64
	search    bool
	streaming bool
	boundary  string
	// size is X-Text-Size of a plain answer.
	size int64
	// more is X-More-Data of a plain answer.
	more bool
	// counted means a plain answer's X-Text-Size is where its body ends.
	counted bool
	// mayOverrun means a plain answer's body may run past X-Text-Size.
	mayOverrun bool
}

// openProgressive asks for a progressiveText log from start, preferring the
// multipart answer. A negative start asks a multipart server for the tail. A
// plain answer showing a log shorter than start fails with errLogRestarted
// before its body is read.
func (c *Client) openProgressive(ctx context.Context, path string, start int64) (*progressiveAnswer, error) {
	return c.openProgressiveQuery(ctx, path, start, nil)
}

// openProgressiveQuery is openProgressive with more query parameters. With
// searchNewLineUntil a multipart server may answer from a later start.
func (c *Client) openProgressiveQuery(ctx context.Context, path string, start int64, query url.Values) (*progressiveAnswer, error) {
	q := url.Values{"start": {strconv.FormatInt(start, 10)}}
	for k, v := range query {
		q[k] = v
	}
	resp, err := c.getContext(ctx, path, q, http.Header{"Accept": {"multipart/form-data"}})
	if err != nil {
		return nil, err
	}
	a := &progressiveAnswer{resp: resp, start: start, search: query.Has("searchNewLineUntil")}
	mediaType, params, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "multipart/form-data" {
		a.streaming, a.boundary = true, params["boundary"]
		return a, nil
	}
	a.size, err = strconv.ParseInt(resp.Header.Get("X-Text-Size"), 10, 64)
	if err != nil {
		a.close()
		return nil, fmt.Errorf("no usable X-Text-Size: %w", err)
	}
	if a.size < start {
		a.close()
		return nil, fmt.Errorf("%w: asked for %d, log ends at %d", errLogRestarted, start, a.size)
	}
	a.more = resp.Header.Get("X-More-Data") == "true"
	a.counted = !a.more
	if a.more {
		version := resp.Header.Get("X-Jenkins")
		if version == "" {
			version = c.serverVersion(ctx)
		}
		a.counted = countsTextSize(version)
		a.mayOverrun = !a.counted && overrunsTextSize(version)
	}
	return a, nil
}

func (a *progressiveAnswer) close() { _ = a.resp.Body.Close() }

// progressiveText is what copying an answer's text found.
type progressiveText struct {
	// more means the log may still grow.
	more bool
	// start is the stored offset the text starts at.
	start int64
	// fromLineStart means a multipart server moved a negative start forward
	// to a line start.
	fromLineStart bool
	// end is the stored offset the text stops at, when exact.
	end   int64
	exact bool
	// size is the stored length when the server answered. While the log is
	// written the text stops at the last line end before it, which is size
	// itself when the log ended in one then.
	size int64
	// capped means a plain body for a running log may have stopped at the
	// line cap, short of the last line end.
	capped bool
	// overran means a plain body for a running log ran past size, as Stapler
	// 1979 to 2028 did when the log grew while it answered.
	overran bool
	// heldCR means a plain body for a running log ended in a CR, which is not
	// written: the next byte decides whether it is half of a CRLF. Asked from
	// the byte after it, Stapler would turn a following LF into CRLF, so
	// nothing could tell the two apart; the next read starts at the CR.
	heldCR bool
}

// copyText copies the answer's text to w, plain bodies with their line ends
// restored.
func (a *progressiveAnswer) copyText(w io.Writer) (progressiveText, error) {
	if a.streaming {
		return a.copyStreaming(w)
	}
	counter := &plainCounter{}
	lf := &lfWriter{w: w}
	var body io.Writer = lf
	if a.more && !a.counted {
		// A body cut at its size may end mid-line, and its last character
		// decoded from half its bytes. The next read from the anchor sends
		// that line again, whole.
		body = &wholeLines{w: lf}
	}
	if _, err := io.Copy(io.MultiWriter(counter, body), a.resp.Body); err != nil {
		return progressiveText{}, err
	}
	t := progressiveText{more: a.more, start: a.start, size: a.size, heldCR: a.more && lf.pendingCR}
	if !t.heldCR {
		if err := lf.Flush(); err != nil {
			return progressiveText{}, err
		}
	}
	if a.counted {
		t.end, t.exact = a.size, true
	}
	if a.more {
		// Stapler counts a CR and an LF as a line end each, and a CRLF
		// received may be one stored LF, so this over-counts and the cap is
		// assumed early.
		t.capped = counter.lineEnds >= maxLinesRead
		// Each CRLF received is a stored LF or CRLF, and each U+FFFD at least
		// one stored byte Jenkins could not decode, so the body stands for at
		// least that many stored bytes.
		t.overran = counter.storedAtLeast() > a.size-a.start
	}
	return t, nil
}

func (a *progressiveAnswer) copyStreaming(w io.Writer) (progressiveText, error) {
	mr := multipart.NewReader(a.resp.Body, a.boundary)
	text, err := mr.NextPart()
	if err != nil {
		return progressiveText{}, err
	}
	if text.FormName() != "text" {
		return progressiveText{}, fmt.Errorf("streaming answer starts with part %q, not text", text.FormName())
	}
	if _, err := io.Copy(w, text); err != nil {
		return progressiveText{}, err
	}
	meta, err := mr.NextPart()
	if err != nil {
		return progressiveText{}, fmt.Errorf("streaming answer has no meta part: %w", err)
	}
	var m struct {
		Completed        bool   `json:"completed"`
		Start            *int64 `json:"start"`
		End              *int64 `json:"end"`
		StartFromNewLine bool   `json:"startFromNewLine"`
	}
	if err := json.NewDecoder(meta).Decode(&m); err != nil {
		return progressiveText{}, fmt.Errorf("decoding streaming meta: %w", err)
	}
	if m.End == nil {
		return progressiveText{}, errors.New("streaming meta has no end offset")
	}
	start := a.start
	if m.Start != nil {
		start = *m.Start
	}
	// A negative start asks for the tail and is answered from where it lands,
	// and searchNewLineUntil moves it forward.
	if a.start >= 0 && (start < a.start || start != a.start && !a.search) {
		return progressiveText{}, fmt.Errorf("%w: asked for %d, got %d", errLogRestarted, a.start, start)
	}
	return progressiveText{
		more: !m.Completed, start: start, fromLineStart: m.StartFromNewLine,
		end: *m.End, exact: m.Completed, size: *m.End,
	}, nil
}

// plainCounter counts what a plain body holds before its line ends are
// restored.
type plainCounter struct {
	bytes, crlfs, replacements, lineEnds int64
	// last holds the two bytes before the current one.
	last [2]byte
}

func (p *plainCounter) Write(b []byte) (int, error) {
	for _, c := range b {
		switch {
		case c == '\n':
			p.lineEnds++
			if p.last[1] == '\r' {
				p.crlfs++
			}
		case c == '\r':
			p.lineEnds++
		case c == 0xBD && p.last == [2]byte{0xEF, 0xBF}:
			p.replacements++
		}
		p.last = [2]byte{p.last[1], c}
	}
	p.bytes += int64(len(b))
	return len(b), nil
}

// storedAtLeast is the fewest stored bytes the body can stand for: a CRLF may
// be a stored LF, and a U+FFFD one undecodable byte.
func (p *plainCounter) storedAtLeast() int64 {
	return p.bytes - p.crlfs - 2*p.replacements
}

// wholeLines passes on text up to its last CR or LF and holds back the rest.
type wholeLines struct {
	w    io.Writer
	held []byte
}

func (h *wholeLines) Write(p []byte) (int, error) {
	h.held = append(h.held, p...)
	i := bytes.LastIndexAny(h.held, "\r\n")
	if i < 0 {
		return len(p), nil
	}
	if _, err := h.w.Write(h.held[:i+1]); err != nil {
		return 0, err
	}
	h.held = append(h.held[:0], h.held[i+1:]...)
	return len(p), nil
}

// countsTextSize reports whether a Jenkins version answers a plain
// progressiveText request for an unfinished log with the offset it read up to.
// Stapler 1979 (#657), first in Jenkins 2.509, sends the stored length instead.
// An unknown version is not trusted.
func countsTextSize(version string) bool {
	major, rest, ok := strings.Cut(version, ".")
	minor, _, _ := strings.Cut(rest, ".")
	maj, err1 := strconv.Atoi(major)
	mnr, err2 := strconv.Atoi(minor)
	if !ok || err1 != nil || err2 != nil {
		return false
	}
	return maj < 2 || maj == 2 && mnr < 509
}

// overrunsTextSize reports whether a Jenkins version may answer a plain
// progressiveText request for an unfinished log with a body that runs past
// X-Text-Size: Stapler 1979 to 2028 (#703 fixed it, first in Jenkins 2.527)
// read on while the log grew during the answer. LTS 2.516 has it too. An
// unknown version is assumed to.
func overrunsTextSize(version string) bool {
	major, rest, ok := strings.Cut(version, ".")
	minor, _, _ := strings.Cut(rest, ".")
	maj, err1 := strconv.Atoi(major)
	mnr, err2 := strconv.Atoi(minor)
	if !ok || err1 != nil || err2 != nil {
		return true
	}
	return maj == 2 && mnr >= 509 && mnr < 527
}

// lfWriter undoes Stapler's LF-to-CRLF rewrite of plain progressiveText, so
// the text matches what the build wrote. It drops every CR before an LF, so a
// CRLF the build itself wrote comes out as LF. A CR ending one write is held
// until the next shows what follows.
type lfWriter struct {
	w         io.Writer
	pendingCR bool
}

func (l *lfWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	buf := make([]byte, 0, len(p)+1)
	if l.pendingCR && p[0] != '\n' {
		buf = append(buf, '\r')
	}
	l.pendingCR = false
	for i, b := range p {
		if b == '\r' {
			if i == len(p)-1 {
				l.pendingCR = true
				continue
			}
			if p[i+1] == '\n' {
				continue
			}
		}
		buf = append(buf, b)
	}
	if _, err := l.w.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush writes a held CR. Only the end of a complete log settles that it is
// not half of a CRLF.
func (l *lfWriter) Flush() error {
	if !l.pendingCR {
		return nil
	}
	l.pendingCR = false
	_, err := l.w.Write([]byte{'\r'})
	return err
}

// ProgressiveLog reads a progressiveText log poll after poll, writing each
// byte of text once.
//
// Only a complete log, or a Jenkins up to 2.508 answering plain, says exactly
// where the text it sent stops. Otherwise the text stops at the last line end
// and the server reports only the stored length, which runs past it by any
// unterminated last line. So the reader keeps an anchor, a stored offset it
// knows the text up to, and how much text past the anchor it has written. The
// stored length the last answer reported is a candidate when that answer's
// text ran up to the last line end before it, and a poll moves the anchor to
// where that text stopped before reading on:
//
//   - On a multipart server, the last LF before the candidate is found with
//     searchNewLineUntil, which Stapler answers by reading the stored bytes,
//     console notes included. The text written past the anchor stops right
//     after it. See placeByNewline.
//   - On a plain one, the candidate is checked by asking from one byte before
//     it: plain text keeps notes, so the text begins with a newline exactly
//     when the stored byte is one.
//
// When the anchor cannot move, the poll asks from the anchor again and writes
// only the text past what it wrote.
//
// A plain answer under Jenkins 2.509 to 2.533 also stops after maxLinesRead
// line ends. If the anchor falls that far behind while the log is written, no
// answer from it can reach further, and the reader waits for the log to
// complete; see Stalled.
type ProgressiveLog struct {
	c    *Client
	path string
	// anchor is a stored offset the text before which is written.
	anchor int64
	// written is how much text past anchor is written.
	written int64
	// candidate is a stored length an earlier answer reported, when that
	// answer's text ran up to the last line end before it and not into a held
	// CR.
	candidate int64
	// lineEndAtCandidate means the last placement found the LF right before
	// the candidate.
	lineEndAtCandidate bool
	streaming          bool
	caughtUp           bool
	stalled            bool
	// explain turns a request failure into the caller's error.
	explain func(error) error
}

// NewProgressiveLog reads the progressiveText log at path, from byte offset
// start on.
func (c *Client) NewProgressiveLog(path string, start int64) *ProgressiveLog {
	return &ProgressiveLog{c: c, path: path, anchor: start, candidate: -1, lineEndAtCandidate: true}
}

// Read writes the text that is new since the last Read. more reports that the
// log may still grow. Cancelling ctx aborts it with ctx.Err().
func (l *ProgressiveLog) Read(ctx context.Context, w io.Writer) (more bool, err error) {
	more, err = l.read(ctx, w)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		err = ctx.Err()
	case l.explain != nil:
		err = l.explain(err)
	}
	return more, err
}

func (l *ProgressiveLog) read(ctx context.Context, w io.Writer) (bool, error) {
	if l.stalled {
		more, err := l.stillWritten(ctx)
		if more || err != nil {
			return more, err
		}
	}
	if l.candidate > l.anchor {
		place := l.placeByByte
		if l.streaming {
			place = l.placeByNewline
		}
		ok, more, err := place(ctx, w)
		if ok || err != nil {
			return more, err
		}
	}
	a, err := l.c.openProgressive(ctx, l.path, l.anchor)
	if err != nil {
		return false, err
	}
	defer a.close()
	skip := &skipWriter{w: w, skip: l.written}
	t, err := a.copyText(skip)
	if err != nil {
		return false, err
	}
	if skip.seen < l.written {
		return false, fmt.Errorf("%w: text from %d shrank from %d to %d bytes", errLogRestarted, l.anchor, l.written, skip.seen)
	}
	l.written = skip.seen
	l.confirmSize(ctx, a, &t)
	l.settle(a, t)
	return t.more, nil
}

// placeByByte reads a plain answer from one byte before the candidate and
// writes it only when it begins with a newline, which proves the candidate
// is where the written text stops. ok is false when it did not, and then
// nothing is written.
func (l *ProgressiveLog) placeByByte(ctx context.Context, w io.Writer) (ok, more bool, err error) {
	// Nothing written past the anchor: the candidate is inside a line that
	// has no end yet.
	if l.written == 0 {
		return false, false, nil
	}
	a, err := l.c.openProgressive(ctx, l.path, l.candidate-1)
	if err != nil {
		return false, false, err
	}
	defer a.close()
	gate := &lineStartWriter{w: w}
	t, err := a.copyText(gate)
	switch {
	case errors.Is(err, errNotLineStart), err == nil && !gate.passed:
		return false, false, nil
	case err != nil:
		return false, false, err
	}
	l.anchor, l.written = l.candidate, gate.n
	l.confirmSize(ctx, a, &t)
	l.settle(a, t)
	return true, t.more, nil
}

// confirmSize marks as overrun a plain answer whose body may have run past
// its size undetected: the stored length of a body cannot be told exactly,
// as a CRLF received may be a stored LF. Unless the log is still that size
// after the answer, and so cannot have grown during it, its size is not a
// candidate. A failed check counts as growth.
func (l *ProgressiveLog) confirmSize(ctx context.Context, a *progressiveAnswer, t *progressiveText) {
	// A held CR or nothing written past the anchor leaves no candidate to
	// confirm.
	if !a.mayOverrun || !t.more || t.exact || t.capped || t.overran || t.heldCR || l.written == 0 {
		return
	}
	b, err := l.c.openProgressive(ctx, l.path, t.size)
	if err != nil {
		t.overran = true
		return
	}
	b.close()
	t.overran = b.streaming || b.size != t.size
}

// maxProbeText bounds the text a multipart probe answer is held for until its
// meta part says whether it is used. A larger one leaves the anchor where it
// is for this poll.
const maxProbeText = 16 << 20

// errProbeTooLong stops holding a probe answer past maxProbeText.
var errProbeTooLong = errors.New("probe answer too long to hold")

// probe is a multipart answer from x with searchNewLineUntil set past the
// candidate: from the line start after the first LF at or after x and before
// the candidate, when there is one.
type probe struct {
	found bool
	a     *progressiveAnswer
	t     progressiveText
	text  []byte
}

func (l *ProgressiveLog) probe(ctx context.Context, x int64) (*probe, error) {
	// Stapler's findNextLineStart checks offsets up to searchNewLineUntil-2.
	a, err := l.c.openProgressiveQuery(ctx, l.path, x,
		url.Values{"searchNewLineUntil": {strconv.FormatInt(l.candidate+1, 10)}})
	if err != nil {
		return nil, err
	}
	defer a.close()
	if !a.streaming {
		return nil, errors.New("searchNewLineUntil needs a multipart answer")
	}
	buf := &limitedBuffer{max: maxProbeText}
	t, err := a.copyText(buf)
	if err != nil {
		return nil, err
	}
	if t.start > l.candidate {
		return nil, fmt.Errorf("streaming answer moved start %d past %d", t.start, l.candidate)
	}
	return &probe{found: t.start > x, a: a, t: t, text: buf.b}, nil
}

// placeByNewline moves the anchor to the line start after the last LF before
// the candidate, which is where the text written past the anchor stops, and
// writes the text from there. Every answer carries the text from the line
// start it found to the end of the log, so the search keeps the asks few:
//
//   - The candidate's own last byte first, when the last poll found the LF
//     there: a log written in whole lines, as a pipeline step's is, ends in
//     one at every poll.
//   - Then where the LF would be if the stored bytes were the text written,
//     and the byte after the LF that answer found: with no console note or
//     undecodable byte since the anchor, a log found mid-line takes these two.
//   - Otherwise notes put the LF later and bytes Jenkins decodes to U+FFFD
//     earlier: strides doubling down from the candidate, then halving.
//
// A found answer starts after the first LF at or after where it was asked
// from, so it moves the lower bound to that LF rather than to the ask.
//
// The answer from the last LF is the text to write. ok is false when there is
// no LF or an answer is too long to hold, and then nothing is written.
func (l *ProgressiveLog) placeByNewline(ctx context.Context, w io.Writer) (ok, more bool, err error) {
	if l.written == 0 {
		return false, false, nil
	}
	// The last LF lies in (lo, hi] until one is found at lo; then in [lo, hi].
	lo, hi := l.anchor-1, l.candidate-1
	var best *probe
	test := func(x int64) (bool, error) {
		p, err := l.probe(ctx, x)
		if err != nil {
			return false, err
		}
		if p.found {
			// The answer starts after the first LF at or after x.
			lo, best = p.t.start-1, p
		} else {
			hi = x - 1
		}
		return p.found, nil
	}
	if l.lineEndAtCandidate {
		if _, err := test(hi); err != nil {
			return l.notPlaced(err)
		}
	}
	if lo < hi {
		guess := min(max(l.anchor+l.written-1, lo+1), hi)
		found, err := test(guess)
		if err != nil {
			return l.notPlaced(err)
		}
		if found && lo < hi {
			if _, err := test(lo + 1); err != nil {
				return l.notPlaced(err)
			}
		}
	}
	for step := int64(1); lo < hi; step *= 2 {
		found, err := test(max(hi-step+1, lo+1))
		if err != nil {
			return l.notPlaced(err)
		}
		if found {
			break
		}
	}
	for lo < hi {
		if _, err := test(lo + (hi-lo+1)/2); err != nil {
			return l.notPlaced(err)
		}
	}
	if best == nil {
		return false, false, nil
	}
	if _, err := w.Write(best.text); err != nil {
		return false, false, err
	}
	l.lineEndAtCandidate = lo == l.candidate-1
	l.anchor, l.written = best.t.start, int64(len(best.text))
	l.settle(best.a, best.t)
	return true, best.t.more, nil
}

// notPlaced reports a failed placement: an answer too long to hold leaves the
// anchor where it is, other errors fail the read.
func (l *ProgressiveLog) notPlaced(err error) (ok, more bool, _ error) {
	if errors.Is(err, errProbeTooLong) {
		return false, false, nil
	}
	return false, false, err
}

func (l *ProgressiveLog) settle(a *progressiveAnswer, t progressiveText) {
	l.streaming = a.streaming
	l.candidate, l.caughtUp = -1, !t.capped
	// No answer from the same anchor can reach past the line cap while the
	// log is written. One that ran past the size reached further than the
	// size says, so it sets no candidate, and the next poll asks from the
	// anchor again.
	l.stalled = !t.exact && t.capped
	switch {
	case t.exact && t.heldCR:
		l.anchor, l.written = t.end-1, 0
	case t.exact:
		l.anchor, l.written = t.end, 0
	case !t.capped && !t.overran && !t.heldCR:
		// A body ending in a CR may stop between the halves of a CRLF, and
		// asking from the byte before the size would then see the LF.
		l.candidate = t.size
	}
}

// stillWritten checks, without reading text, whether a stalled log is still
// being written.
func (l *ProgressiveLog) stillWritten(ctx context.Context) (bool, error) {
	a, err := l.c.openProgressive(ctx, l.path, l.anchor)
	if err != nil {
		return false, err
	}
	a.close()
	return !a.streaming && a.more, nil
}

// CaughtUp reports that the last Read reached the last line end the log had
// then.
func (l *ProgressiveLog) CaughtUp() bool { return l.caughtUp }

// Stalled reports that the log is being written more than maxLinesRead lines
// past the anchor on a server that does not say where a plain answer stops.
// Nothing more is written until the log completes.
func (l *ProgressiveLog) Stalled() bool { return l.stalled }

// skipWriter drops the first skip bytes and counts every byte offered.
type skipWriter struct {
	w          io.Writer
	skip, seen int64
}

func (s *skipWriter) Write(p []byte) (int, error) {
	n := len(p)
	from := min(max(s.skip-s.seen, 0), int64(n))
	s.seen += int64(n)
	if from < int64(n) {
		if _, err := s.w.Write(p[from:]); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// lineStartWriter passes text on only when it begins with a newline, which it
// drops. n counts what it passed on.
type lineStartWriter struct {
	w      io.Writer
	passed bool
	n      int64
}

func (g *lineStartWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	rest := p
	if !g.passed {
		if p[0] != '\n' {
			return 0, errNotLineStart
		}
		g.passed = true
		rest = p[1:]
	}
	if len(rest) > 0 {
		if _, err := g.w.Write(rest); err != nil {
			return 0, err
		}
		g.n += int64(len(rest))
	}
	return len(p), nil
}

// limitedBuffer holds up to max bytes and fails past them.
type limitedBuffer struct {
	b   []byte
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if len(l.b)+len(p) > l.max {
		return 0, errProbeTooLong
	}
	l.b = append(l.b, p...)
	return len(p), nil
}

// tailBuffer keeps the last max bytes written to it in a ring, all of them
// when max is not positive.
type tailBuffer struct {
	ring  []byte
	max   int
	total int64
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	start := t.total
	t.total += int64(n)
	if t.max <= 0 {
		t.ring = append(t.ring, p...)
		return n, nil
	}
	if len(p) > t.max {
		start += int64(len(p) - t.max)
		p = p[len(p)-t.max:]
	}
	if len(t.ring) < t.max {
		if start == int64(len(t.ring)) && len(t.ring)+len(p) <= t.max {
			t.ring = append(t.ring, p...)
			return n, nil
		}
		t.ring = append(t.ring, make([]byte, t.max-len(t.ring))...)
	}
	// Stream byte k lives at k modulo max.
	pos := int(start % int64(t.max))
	k := copy(t.ring[pos:], p)
	copy(t.ring, p[k:])
	return n, nil
}

func (t *tailBuffer) String() string {
	if t.max <= 0 || len(t.ring) < t.max {
		return string(t.ring)
	}
	pos := int(t.total % int64(t.max))
	return string(t.ring[pos:]) + string(t.ring[:pos])
}

// dropped reports that String leaves out earlier bytes.
func (t *tailBuffer) dropped() bool { return t.max > 0 && t.total > int64(t.max) }
