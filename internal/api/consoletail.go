package api

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

const (
	// minTailWindow is the smallest window a stalled tail read shrinks to
	// before it reads consoleText instead.
	minTailWindow = 4 << 10
	// startTailWindow and defaultTailWindow bound the window ConsoleTailLines
	// grows while it has fewer lines than asked for.
	startTailWindow   = 2 << 20
	defaultTailWindow = 64 << 20
)

// errTailStalled means a tail window holds more lines than a plain answer
// for a running log carries, on a server that does not say where it stopped.
var errTailStalled = errors.New("tail window passes the line cap")

// tailWindow returns about the last window bytes of the console up to its
// last line end, a partial first line dropped. whole reports the text starts
// at the log's first byte.
//
// A multipart server answers a negative start with the tail from its first
// line start, in one request. Other servers are asked for X-Text-Size and
// read from window bytes before it. On Jenkins up to 2.508, X-Text-Size of a
// running log counts only its first 10000 lines, so that read starts early
// and pages forward to the end, keeping only window bytes.
func (c *Client) tailWindow(ctx context.Context, jobPath string, number int, window int64) (text string, whole bool, err error) {
	path := consolePath(jobPath, number)
	if !c.plainProgressive.Load() {
		text, whole, ok, err := c.streamingTail(ctx, path, window)
		if ok || err != nil {
			return text, whole, err
		}
		c.plainProgressive.Store(true)
	}

	size, err := c.progressiveSize(path)
	if err != nil {
		return "", false, err
	}
	start := max(size-window, 0)
	l := c.NewProgressiveLog(path, start)
	buf := &tailBuffer{max: int(window)}
	for {
		more, err := l.Read(ctx, buf)
		if err != nil {
			return "", false, err
		}
		if l.Stalled() {
			return "", false, errTailStalled
		}
		if !more || l.CaughtUp() {
			break
		}
	}
	text = buf.String()
	if start > 0 || buf.dropped() {
		text = dropFirstLine(text)
	}
	return text, start == 0 && !buf.dropped(), nil
}

// streamingTail asks for the tail with a negative start. ok is false when the
// server does not answer multipart. Jenkins 2.568.3 answers a plain negative
// start with headers and no text; a server error is taken the same way.
func (c *Client) streamingTail(ctx context.Context, path string, window int64) (text string, whole, ok bool, err error) {
	a, err := c.openProgressive(ctx, path, -window)
	var se *jenkins.ServerError
	switch {
	case errors.As(err, &se):
		return "", false, false, nil
	case err != nil:
		return "", false, false, err
	}
	defer a.close()
	if !a.streaming {
		return "", false, false, nil
	}
	buf := &tailBuffer{max: int(window)}
	t, err := a.copyText(buf)
	if err != nil {
		return "", false, false, err
	}
	text = buf.String()
	if t.start > 0 && !t.fromLineStart || buf.dropped() {
		text = dropFirstLine(text)
	}
	return text, t.start == 0 && !buf.dropped(), true, nil
}

// GetBuildLogTail returns about the last maxBytes of the console up to its
// last line end, a partial first line dropped. A window that passes the line
// cap of a running log's plain answers is halved until it does not, and
// below minTailWindow the tail comes from consoleText.
func (c *Client) GetBuildLogTail(jobPath string, number int, maxBytes int64) (string, error) {
	ctx := context.Background()
	for window := maxBytes; ; window /= 2 {
		text, _, err := c.tailWindow(ctx, jobPath, number, window)
		switch {
		case err == nil:
			return text, nil
		case !errors.Is(err, errTailStalled):
			return "", c.consoleErr(jobPath, err)
		case window/2 < minTailWindow:
			lines, err := c.consoleTextTail(jobPath, number, 0, maxBytes)
			return strings.Join(lines, ""), err
		}
	}
}

// ConsoleTailLines returns the last n lines of the console up to its last
// line end, sanitized and without their newlines. It reads a tail window,
// doubled while it holds fewer than n lines. On a server whose plain answers
// for a running log stop after 10000 lines without saying where, the window
// is halved instead until it fits, and when that cannot hold n lines the
// whole consoleText is read. truncated reports that the largest window
// still held fewer than n lines of a longer log, so fewer than n are returned.
func (c *Client) ConsoleTailLines(jobPath string, number, n int) (tail []string, truncated bool, err error) {
	ctx := context.Background()
	window := min(int64(startTailWindow), int64(c.consoleTailWindow))
	shrunk := false
	for {
		text, whole, err := c.tailWindow(ctx, jobPath, number, window)
		if errors.Is(err, errTailStalled) {
			if n >= maxLinesRead || window/2 < minTailWindow {
				lines, err := c.consoleTextTailLines(jobPath, number, n)
				return lines, false, err
			}
			window, shrunk = window/2, true
			continue
		}
		if err != nil {
			return nil, false, c.consoleErr(jobPath, err)
		}
		lines := splitLines(output.SanitizeLog(text))
		switch {
		case len(lines) >= n || whole || window >= int64(c.consoleTailWindow):
			return lines[max(len(lines)-n, 0):], len(lines) < n && !whole, nil
		case shrunk:
			lines, err := c.consoleTextTailLines(jobPath, number, n)
			return lines, false, err
		}
		window *= 2
	}
}

func (c *Client) consoleTextTailLines(jobPath string, number, n int) ([]string, error) {
	lines, err := c.consoleTextTail(jobPath, number, n, 0)
	if err != nil {
		return nil, err
	}
	for i, l := range lines {
		lines[i] = output.SanitizeLog(strings.TrimSuffix(l, "\n"))
	}
	return lines, nil
}

// consoleTextTail returns the last complete lines of consoleText, newlines
// kept: at most n of them when n is positive, and at most maxBytes when that
// is. Only a running log is read this way, and the progressiveText tails end
// at its last line end, so an unterminated last line is left out.
func (c *Client) consoleTextTail(jobPath string, number, n int, maxBytes int64) ([]string, error) {
	body, err := c.OpenConsoleText(jobPath, number)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	var ring []string
	var size int64
	r := bufio.NewReaderSize(body, 64<<10)
	for {
		line, err := r.ReadString('\n')
		if strings.HasSuffix(line, "\n") {
			ring = append(ring, line)
			size += int64(len(line))
			for len(ring) > 0 && (n > 0 && len(ring) > n || maxBytes > 0 && size > maxBytes) {
				size -= int64(len(ring[0]))
				ring = ring[1:]
			}
		}
		if errors.Is(err, io.EOF) {
			return ring, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading build log: %w", err)
		}
	}
}

func dropFirstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[i+1:]
	}
	return text
}

// splitLines splits text into lines, dropping a single trailing newline so a
// log ending in "\n" does not yield an empty last line.
func splitLines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
