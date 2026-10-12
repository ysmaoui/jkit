package export

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"strconv"
)

//go:embed template.html
var templateHTML string

// page holds the two halves of template.html. The snapshot JSON goes between
// them, kept out of the template: logs can total hundreds of MB, too much to
// pass through html/template as a value. Each half ends in HTML text context,
// which html/template requires, so the data block's tags are written here.
var page = template.Must(template.New("page").Parse(templateHTML))

type pageData struct {
	Title string
}

// Render writes s as one self-contained HTML page that needs no network. It
// is not safe for concurrent calls on the same Snapshot.
func Render(w io.Writer, s *Snapshot) error {
	data := pageData{Title: fmt.Sprintf("%s #%d", s.Job, s.Number)}
	if err := page.ExecuteTemplate(w, "head", data); err != nil {
		return err
	}
	if _, err := io.WriteString(w, `<script type="application/json" id="snapshot">`); err != nil {
		return err
	}
	if err := writeJSON(w, s); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "</script>\n"); err != nil {
		return err
	}
	return page.ExecuteTemplate(w, "tail", data)
}

// writeJSON writes s as json.Encoder would, without holding the logs in
// memory: it encodes s with a placeholder for each spooled log, then copies
// the logs from the spool in place of the placeholders. Base64 text needs no
// JSON escaping, so the bytes match a plain Encode.
//
// SetEscapeHTML (the default) writes <, > and & as \u003c, \u003e and \u0026,
// so no "</script" or "<!--" can occur: inside a script data block those are
// the only sequences the HTML parser acts on, and both need a '<'. JSON.parse
// turns the escapes back into the characters.
func writeJSON(w io.Writer, s *Snapshot) error {
	var logs []*Log
	add := func(l *Log) {
		if l.spool != nil {
			logs = append(logs, l)
		}
	}
	s.EachStep(func(_ *Node, st *Step) { add(&st.Log) })
	add(&s.Console)

	// Random, so no job or step name can pass for a placeholder.
	prefix := "jkit-spool-" + rand.Text() + "-"
	for i, l := range logs {
		l.GzipBase64 = prefix + strconv.Itoa(i)
	}
	var skel bytes.Buffer
	enc := json.NewEncoder(&skel)
	enc.SetEscapeHTML(true)
	err := enc.Encode(s)
	for _, l := range logs {
		l.GzipBase64 = ""
	}
	if err != nil {
		return fmt.Errorf("encoding snapshot: %w", err)
	}

	rest := skel.Bytes()
	marker := []byte(`"` + prefix)
	buf := make([]byte, 64<<10)
	for {
		i := bytes.Index(rest, marker)
		if i < 0 {
			break
		}
		if _, err := w.Write(rest[:i+1]); err != nil {
			return err
		}
		rest = rest[i+len(marker):]
		end := bytes.IndexByte(rest, '"')
		n, err := strconv.Atoi(string(rest[:end]))
		if err != nil || n >= len(logs) {
			return fmt.Errorf("encoding snapshot: bad log placeholder %q", rest[:end])
		}
		l := logs[n]
		copied, err := io.CopyBuffer(w, l.content(), buf)
		if err == nil && copied != l.n {
			err = fmt.Errorf("got %d of %d bytes", copied, l.n)
		}
		if err != nil {
			return fmt.Errorf("copying log from spool: %w", err)
		}
		rest = rest[end:]
	}
	_, err = w.Write(rest)
	return err
}
