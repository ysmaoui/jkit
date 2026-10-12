package export

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
)

//go:embed template.html
var templateHTML string

// page holds the two halves of template.html. The snapshot JSON goes between
// them, kept out of the template: logs can total tens of MB, too much to pass
// through html/template as a value. json.Encoder marshals the whole snapshot
// into one buffer before writing, so peak memory is about the JSON size on top
// of the snapshot itself. Each half ends in HTML text context, which
// html/template requires, so the data block's tags are written here.
var page = template.Must(template.New("page").Parse(templateHTML))

type pageData struct {
	Title string
}

// Render writes s as one self-contained HTML page that needs no network.
func Render(w io.Writer, s *Snapshot) error {
	data := pageData{Title: fmt.Sprintf("%s #%d", s.Job, s.Number)}
	if err := page.ExecuteTemplate(w, "head", data); err != nil {
		return err
	}
	if _, err := io.WriteString(w, `<script type="application/json" id="snapshot">`); err != nil {
		return err
	}
	// SetEscapeHTML (the default) writes <, > and & as \u003c, \u003e and
	// \u0026, so no "</script" or "<!--" can occur: inside a script data
	// block those are the only sequences the HTML parser acts on, and both
	// need a '<'. JSON.parse turns the escapes back into the characters.
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(s); err != nil {
		return fmt.Errorf("encoding snapshot: %w", err)
	}
	if _, err := io.WriteString(w, "</script>\n"); err != nil {
		return err
	}
	return page.ExecuteTemplate(w, "tail", data)
}
