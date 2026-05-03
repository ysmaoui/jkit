package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/template"

	"github.com/charmbracelet/x/ansi"
)

type Column struct {
	Header string
	Field  func(any) string
}

type Formatter struct {
	writer   io.Writer
	isJSON   bool
	template string
}

func NewFormatter(w io.Writer, isJSON bool, tmpl string) *Formatter {
	return &Formatter{writer: w, isJSON: isJSON, template: tmpl}
}

func (f *Formatter) Output(data any, columns []Column) error {
	if f.isJSON {
		return f.outputJSON(data)
	}
	if f.template != "" {
		return f.outputTemplate(data)
	}
	items, ok := data.([]any)
	if !ok {
		return f.outputJSON(data)
	}
	return f.outputTable(items, columns)
}

func (f *Formatter) outputJSON(data any) error {
	enc := json.NewEncoder(f.writer)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

func (f *Formatter) outputTemplate(data any) error {
	t, err := template.New("").Parse(f.template)
	if err != nil {
		return fmt.Errorf("parsing template: %w", err)
	}
	return t.Execute(f.writer, data)
}

func (f *Formatter) outputTable(items []any, columns []Column) error {
	if len(columns) == 0 || len(items) == 0 {
		return nil
	}

	// Calculate column widths using visual width (ANSI-aware)
	widths := make([]int, len(columns))
	for i, c := range columns {
		widths[i] = ansi.StringWidth(c.Header)
	}
	rows := make([][]string, len(items))
	for i, item := range items {
		row := make([]string, len(columns))
		for j, c := range columns {
			val := c.Field(item)
			row[j] = val
			if w := ansi.StringWidth(val); w > widths[j] {
				widths[j] = w
			}
		}
		rows[i] = row
	}

	// Print header
	hdrs := make([]string, len(columns))
	for i, c := range columns {
		hdrs[i] = pad(c.Header, widths[i])
	}
	_, _ = fmt.Fprintln(f.writer, strings.Join(hdrs, "  "))

	// Print rows
	for _, row := range rows {
		cols := make([]string, len(row))
		for i, val := range row {
			cols[i] = pad(val, widths[i])
		}
		_, _ = fmt.Fprintln(f.writer, strings.Join(cols, "  "))
	}
	return nil
}

func pad(s string, width int) string {
	w := ansi.StringWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}
