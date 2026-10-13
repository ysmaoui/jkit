package export

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"strings"
	"sync"

	"github.com/ysmaoui/jkit/internal/api"
)

// errNoNodeSpans means the annotated console had no pipeline-node span, as
// with log storage other than Jenkins' own file, a freestyle build, or a
// pipeline none of whose steps wrote anything. Step logs must then be read
// one by one.
var errNoNodeSpans = errors.New("console has no pipeline node spans")

// Bounds on a held tag and entity. Anything longer is not markup Jenkins
// writes, and is passed on as text.
const (
	maxTagBytes    = 64 << 10
	maxEntityBytes = 32
)

type splitState int

const (
	inText splitState = iota
	inTag
	inComment
	inEntity
)

// consoleSplitter turns progressiveHtml into text, per pipeline node, as it
// streams by. Text inside <span class="pipeline-node-ID"> is that node's;
// the rest, the [Pipeline] lines among it, is the build's own, emitted with
// node "". Tags and comments are dropped (AnsiColor keeps escape sequences in
// comments, which the export sanitizer would strip anyway), entities are
// decoded, and Timestamper's visible <span class="timestamp"> is dropped
// while its hidden [ISO] prefix is kept, since progressiveText carries that
// prefix and not the visible clock.
//
// emit gets text in pieces, interleaved between nodes as parallel branches
// wrote it, and must not keep the slice.
type consoleSplitter struct {
	emit func(node string, text []byte) error
	// newNode, when set, gets the attributes of each pipeline-new-node span,
	// which opens every flow node's [Pipeline] line.
	newNode func(id, start, enclosing string)

	state splitState
	held  []byte
	// quote is the quote character an attribute value in a held tag is open
	// in, 0 outside one.
	quote byte
	// dashes counts the '-' just seen in a comment, up to 2.
	dashes int

	// spans holds, per open span, the node it opens or "" for one that opens
	// none; dropped spans are marked with dropSpan.
	spans    []string
	node     string
	dropping int
	sawNode  bool

	out []byte
}

// dropSpan marks an open span whose text is dropped. It cannot be a node ID:
// those come from a class token, which holds no space.
const dropSpan = " drop"

func newConsoleSplitter(emit func(node string, text []byte) error) *consoleSplitter {
	return &consoleSplitter{emit: emit}
}

func (s *consoleSplitter) Write(p []byte) (int, error) {
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch s.state {
		case inText:
			// Plain text runs are passed on in one piece.
			j := i
			for j < len(p) && p[j] != '<' && p[j] != '&' {
				j++
			}
			s.text(p[i:j])
			if j == len(p) {
				i = j
				continue
			}
			s.held = append(s.held[:0], p[j])
			s.state = inTag
			if p[j] == '&' {
				s.state = inEntity
			}
			i = j
		case inTag:
			s.held = append(s.held, c)
			switch {
			case s.quote != 0:
				if c == s.quote {
					s.quote = 0
				}
			case c == '"' || c == '\'':
				s.quote = c
			case c == '>':
				if err := s.tag(s.held); err != nil {
					return 0, err
				}
				s.state = inText
			}
			if s.state == inTag {
				if bytes.Equal(s.held, []byte("<!--")) {
					s.state, s.dashes = inComment, 0
				} else if len(s.held) > maxTagBytes {
					s.text(s.held)
					s.state, s.quote = inText, 0
				}
			}
		case inComment:
			switch {
			case c == '>' && s.dashes == 2:
				s.state = inText
			case c == '-':
				s.dashes = min(s.dashes+1, 2)
			default:
				s.dashes = 0
			}
		case inEntity:
			s.held = append(s.held, c)
			switch {
			case c == ';':
				s.text([]byte(html.UnescapeString(string(s.held))))
				s.state = inText
			case !isEntityByte(c) || len(s.held) > maxEntityBytes:
				// Not an entity after all: the held bytes are text, and the
				// last one is looked at again.
				s.text([]byte(html.UnescapeString(string(s.held[:len(s.held)-1]))))
				s.state = inText
				i--
			}
		}
		if err := s.flushIfFull(); err != nil {
			return 0, err
		}
	}
	return len(p), s.flush()
}

// Close passes on what is held and reports errNoNodeSpans when no node span
// was seen.
func (s *consoleSplitter) Close() error {
	switch s.state {
	case inTag:
		s.text(s.held)
	case inEntity:
		s.text([]byte(html.UnescapeString(string(s.held))))
	}
	s.state = inText
	if err := s.flush(); err != nil {
		return err
	}
	if !s.sawNode {
		return errNoNodeSpans
	}
	return nil
}

func isEntityByte(c byte) bool {
	return c == '#' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func (s *consoleSplitter) text(b []byte) {
	if s.dropping == 0 {
		s.out = append(s.out, b...)
	}
}

// flushIfFull keeps the pending piece small when one node writes a lot.
func (s *consoleSplitter) flushIfFull() error {
	if len(s.out) < 1<<16 {
		return nil
	}
	return s.flush()
}

func (s *consoleSplitter) flush() error {
	if len(s.out) == 0 {
		return nil
	}
	err := s.emit(s.node, s.out)
	s.out = s.out[:0]
	return err
}

// tag acts on a whole tag. Only spans matter: they carry the node and the
// timestamp, and Jenkins nests nothing else across them.
func (s *consoleSplitter) tag(t []byte) error {
	name, attrs := tagName(t)
	if !strings.EqualFold(name, "span") {
		return nil
	}
	if t[1] == '/' {
		if len(s.spans) == 0 {
			return nil
		}
		top := s.spans[len(s.spans)-1]
		s.spans = s.spans[:len(s.spans)-1]
		if top == dropSpan {
			s.dropping--
			return nil
		}
		if top != "" {
			return s.setNode(s.innerNode())
		}
		return nil
	}
	if bytes.HasSuffix(t, []byte("/>")) {
		return nil
	}
	mark := ""
	for _, class := range strings.Fields(attr(attrs, "class")) {
		if id, ok := strings.CutPrefix(class, "pipeline-node-"); ok && id != "" {
			mark = id
		}
		if class == "timestamp" {
			mark = dropSpan
		}
		if class == "pipeline-new-node" && s.newNode != nil {
			s.newNode(attr(attrs, "nodeId"), attr(attrs, "startId"), attr(attrs, "enclosingId"))
		}
	}
	s.spans = append(s.spans, mark)
	switch mark {
	case "":
	case dropSpan:
		s.dropping++
	default:
		s.sawNode = true
		return s.setNode(mark)
	}
	return nil
}

// innerNode is the node of the innermost open node span, "" for none.
func (s *consoleSplitter) innerNode() string {
	for i := len(s.spans) - 1; i >= 0; i-- {
		if m := s.spans[i]; m != "" && m != dropSpan {
			return m
		}
	}
	return ""
}

func (s *consoleSplitter) setNode(node string) error {
	if node == s.node {
		return nil
	}
	if err := s.flush(); err != nil {
		return err
	}
	s.node = node
	return nil
}

// tagName splits a tag, '<' to '>', into its name and the rest.
func tagName(t []byte) (string, string) {
	inner := strings.TrimSuffix(string(t[1:len(t)-1]), "/")
	inner = strings.TrimPrefix(inner, "/")
	name, rest, _ := strings.Cut(inner, " ")
	return name, rest
}

// attr returns an attribute's value from a tag's attribute text, entities
// decoded.
func attr(attrs, name string) string {
	for attrs != "" {
		attrs = strings.TrimLeft(attrs, " \t\r\n")
		eq := strings.IndexAny(attrs, "= \t\r\n")
		if eq < 0 {
			return ""
		}
		key := attrs[:eq]
		attrs = strings.TrimLeft(attrs[eq:], " \t\r\n")
		if !strings.HasPrefix(attrs, "=") {
			continue
		}
		attrs = strings.TrimLeft(attrs[1:], " \t\r\n")
		var val string
		if attrs != "" && (attrs[0] == '"' || attrs[0] == '\'') {
			end := strings.IndexByte(attrs[1:], attrs[0])
			if end < 0 {
				return ""
			}
			val, attrs = attrs[1:1+end], attrs[2+end:]
		} else {
			end := strings.IndexAny(attrs, " \t\r\n")
			if end < 0 {
				end = len(attrs)
			}
			val, attrs = attrs[:end], attrs[end:]
		}
		if strings.EqualFold(key, name) {
			return html.UnescapeString(val)
		}
	}
	return ""
}

// collectFromConsole fills the step logs from one annotated-console download
// while the plain console downloads beside it. When that download cannot
// give every step's log, Notice says why and the steps are read one by one.
func collectFromConsole(ctx context.Context, client *api.Client, jobPath string, number int, opts Options, snap *Snapshot) error {
	consoleSpool, err := newSpool(opts.SpoolDir)
	if err != nil {
		return err
	}
	workers := snap.spools
	snap.spools = append(snap.spools, consoleSpool)

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu    sync.Mutex
		fatal error
	)
	fail := func(err error) {
		mu.Lock()
		fatal = cmp.Or(fatal, err)
		mu.Unlock()
		cancel()
	}

	if opts.Progress != nil {
		opts.Progress(0, 0, nil, nil)
	}
	consoleDone := make(chan struct{})
	go func() {
		defer close(consoleDone)
		if err := readConsole(workCtx, client, jobPath, number, opts.MaxLogBytes, consoleSpool, snap); err != nil {
			fail(err)
		}
	}()

	notice(opts, "reading step logs from the annotated console")
	find := newFailureFinder()
	err = stepLogsFromConsole(workCtx, client, jobPath, number, opts, workers[0], snap.Stages, find)
	var fb *fallbackError
	if errors.As(err, &fb) {
		notice(opts, fmt.Sprintf("annotated console unusable, reading step logs one by one: %v", fb.err))
		find = nil
		err = fillStepLogs(workCtx, client, jobPath, number, opts, workers, snap.Stages)
	}
	if err != nil {
		fail(err)
	} else {
		snap.markFailure(find)
	}
	<-consoleDone
	return cmp.Or(fatal, ctx.Err())
}

func notice(opts Options, msg string) {
	if opts.Notice != nil {
		opts.Notice(msg)
	}
}

// fallbackError is why the annotated console cannot give the step logs.
type fallbackError struct{ err error }

func (e *fallbackError) Error() string { return e.err.Error() }

// textRun is n bytes of one node's text at off in the raw spool.
type textRun struct{ off, n int64 }

// stepLogsFromConsole downloads the annotated console and encodes each step's
// log onto sp. The split text waits in a raw spool file, with only where
// each step's runs lie held in memory, because parallel branches interleave
// and a step's log is whole only once the download ends. Steps without
// output get an empty log and no request. find sees all the split text,
// the build's own and that of nodes that are not steps.
func stepLogsFromConsole(ctx context.Context, client *api.Client, jobPath string, number int, opts Options, sp *spool, nodes []Node, find *failureFinder) error {
	raw, err := newSpool(opts.SpoolDir)
	if err != nil {
		return err
	}
	defer func() { _ = raw.close() }()

	runs := map[string][]textRun{}
	walk(nodes, func(n *Node) {
		for _, s := range n.Steps {
			runs[s.ID] = nil
		}
	})
	split := newConsoleSplitter(func(node string, text []byte) error {
		find.text(node, text)
		rs, ok := runs[node]
		if !ok {
			return nil
		}
		if l := len(rs); l > 0 && rs[l-1].off+rs[l-1].n == raw.size {
			rs[l-1].n += int64(len(text))
		} else {
			rs = append(rs, textRun{raw.size, int64(len(text))})
		}
		runs[node] = rs
		_, _ = raw.Write(text)
		return nil
	})
	split.newNode = find.newNode
	_, partial, err := client.CopyConsoleHTML(ctx, jobPath, number, split)
	if err == nil {
		err = split.Close()
	}
	// With no step to fill, all text is the build's own, as when the
	// Jenkinsfile failed to compile, and find has seen it whole.
	if errors.Is(err, errNoNodeSpans) && len(runs) == 0 {
		err = nil
	}
	switch {
	case err != nil && stopsExport(ctx, err):
		return err
	case err != nil:
		return &fallbackError{withTimeoutHint(err)}
	case partial:
		return &fallbackError{errors.New("the console of a running build may stop short")}
	}
	if err := raw.flush(); err != nil {
		return fmt.Errorf("%w: %w\n%s", errSpool, err, spoolHint)
	}

	var steps []*Step
	walk(nodes, func(n *Node) {
		for i := range n.Steps {
			steps = append(steps, &n.Steps[i])
		}
	})
	for _, s := range steps {
		s.Log, err = encodeLog(sp, opts.MaxLogBytes, func(w io.Writer) error {
			for _, r := range runs[s.ID] {
				if _, err := io.Copy(w, io.NewSectionReader(raw.f, r.off, r.n)); err != nil {
					return fmt.Errorf("%w: reading back: %w", errSpool, err)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return fillExceptions(ctx, client, jobPath, number, opts, steps)
}

// fillExceptions reads the exception text of steps, Concurrency requests at
// once. Each text lands on its own step, so the result does not depend on
// which request answers first. The first fatal error cancels the rest.
func fillExceptions(ctx context.Context, client *api.Client, jobPath string, number int, opts Options, steps []*Step) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu    sync.Mutex
		fatal error
		wg    sync.WaitGroup
	)
	queue := make(chan *Step)
	for range max(opts.Concurrency, 1) {
		wg.Go(func() {
			for s := range queue {
				if err := fillException(workCtx, client, jobPath, number, s); err != nil {
					mu.Lock()
					fatal = cmp.Or(fatal, err)
					mu.Unlock()
					cancel()
				}
			}
		})
	}
dispatch:
	for _, s := range steps {
		select {
		case queue <- s:
		case <-workCtx.Done():
			break dispatch
		}
	}
	close(queue)
	wg.Wait()
	return cmp.Or(fatal, ctx.Err())
}
