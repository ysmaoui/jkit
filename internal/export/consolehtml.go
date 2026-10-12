package export

import (
	"bytes"
	"errors"
	"html"
	"strings"
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
