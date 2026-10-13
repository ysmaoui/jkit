package cmd

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"

	"github.com/ysmaoui/jkit/internal/export"
)

func feedProgress(p *exportProgress, total int) {
	n := &export.Node{Name: "Build"}
	for i := 1; i <= total; i++ {
		p.report(i, total, n, &export.Step{Name: "step"})
	}
	p.report(0, 0, nil, nil)
}

func TestExportProgressPlainIsBounded(t *testing.T) {
	for _, total := range []int{1, 2, 15, 1000, 7284} {
		var buf bytes.Buffer
		feedProgress(&exportProgress{w: &buf}, total)
		lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
		assert.LessOrEqual(t, len(lines), maxPlainProgressLines+3, "total %d", total)
		assert.Contains(t, lines[0], "fetching step 1/")
		assert.Contains(t, buf.String(), "fetching step "+strconv.Itoa(total)+"/"+strconv.Itoa(total))
		assert.Equal(t, "fetching console log", lines[len(lines)-1])
		assert.NotContains(t, buf.String(), "\x1b")
	}
}

func TestExportProgressTTYRewritesOneLine(t *testing.T) {
	var buf bytes.Buffer
	p := &exportProgress{w: &buf, tty: true, width: 80}
	feedProgress(p, 100)
	assert.NotContains(t, buf.String(), "\n")
	assert.Equal(t, 101, strings.Count(buf.String(), "\r\x1b[K"))

	p.notice("warning: x")
	assert.True(t, strings.HasSuffix(buf.String(), "\r\x1b[Kwarning: x\n"))

	before := buf.Len()
	p.clear()
	assert.Equal(t, before, buf.Len(), "nothing left to clear")
}

func TestExportProgressTTYTruncatesToWidth(t *testing.T) {
	var buf bytes.Buffer
	p := &exportProgress{w: &buf, tty: true, width: 20}
	p.report(1, 9, &export.Node{Name: "A very long stage name"}, &export.Step{Name: "step"})
	text := strings.TrimPrefix(buf.String(), "\r\x1b[K")
	assert.Less(t, runewidth.StringWidth(text), 20)

	buf.Reset()
	p.report(1, 9, &export.Node{Name: "ビルドステージ名前"}, &export.Step{Name: "ステップ名前"})
	text = strings.TrimPrefix(buf.String(), "\r\x1b[K")
	assert.Less(t, runewidth.StringWidth(text), 20)
}

func TestRewriteInPlace(t *testing.T) {
	assert.True(t, rewriteInPlace(true, "xterm", false))
	assert.True(t, rewriteInPlace(true, "", false))
	assert.False(t, rewriteInPlace(false, "xterm", false))
	assert.False(t, rewriteInPlace(true, "dumb", false))
	assert.False(t, rewriteInPlace(true, "xterm", true))
}
