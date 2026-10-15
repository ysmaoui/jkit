package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// CopyConsoleHTML writes a build's annotated console, as progressiveHtml
// serves it, to w and returns how many bytes it wrote. Line ends are restored
// as for progressiveText. With file log storage each node's output is wrapped
// in <span class="pipeline-node-ID">, so one download carries every step log.
//
// A complete build is one answer. A running build is read to its last line
// end and not followed. Plain answers for a running log stop after
// maxLinesRead line ends; where Jenkins up to 2.508 reports the offset it
// read up to, the copy goes on from there, carrying X-ConsoleAnnotator so
// the next answer continues the markup. Later Jenkins reports the stored
// length instead, which runs past what was sent, so the copy stops and
// partial reports that it may be short.
func (c *Client) CopyConsoleHTML(ctx context.Context, jobPath string, number int, w io.Writer) (n int64, partial bool, err error) {
	path := fmt.Sprintf("%s/%d/logText/progressiveHtml", NormalizeJobPath(jobPath), number)
	cw := &byteCounter{w: w}
	// One across answers: a CR ending one may pair with an LF opening the
	// next.
	lf := &lfWriter{w: cw}
	var start int64
	var annotator string
	for {
		var header http.Header
		if annotator != "" {
			header = http.Header{"X-ConsoleAnnotator": {annotator}}
		}
		resp, err := c.getContext(ctx, path, url.Values{"start": {strconv.FormatInt(start, 10)}}, header)
		if err != nil {
			return cw.n, false, fmt.Errorf("reading console html: %w", err)
		}
		more, capped, size, err := copyConsoleAnswer(resp, lf)
		_ = resp.Body.Close()
		if err != nil {
			return cw.n, false, fmt.Errorf("reading console html: %w", err)
		}
		if !more || !capped {
			err := lf.Flush()
			return cw.n, false, err
		}
		version := resp.Header.Get("X-Jenkins")
		if version == "" {
			version = c.serverVersion(ctx)
		}
		if !countsTextSize(version) || size <= start {
			err := lf.Flush()
			return cw.n, true, err
		}
		start, annotator = size, resp.Header.Get("X-ConsoleAnnotator")
	}
}

// copyConsoleAnswer copies one answer's body. capped means it may have
// stopped at the line cap.
func copyConsoleAnswer(resp *http.Response, w io.Writer) (more, capped bool, size int64, err error) {
	size, err = strconv.ParseInt(resp.Header.Get("X-Text-Size"), 10, 64)
	if err != nil {
		return false, false, 0, fmt.Errorf("no usable X-Text-Size: %w", err)
	}
	more = resp.Header.Get("X-More-Data") == "true"
	counter := &plainCounter{}
	if _, err := io.Copy(io.MultiWriter(counter, w), resp.Body); err != nil {
		return false, false, 0, err
	}
	// Markup adds no line ends. A CRLF counts twice, so the cap is assumed
	// early.
	return more, more && counter.lineEnds >= maxLinesRead, size, nil
}
