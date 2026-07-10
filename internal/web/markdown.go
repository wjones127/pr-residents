package web

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// mdRenderer turns review markdown (summaries, comment bodies) into HTML for
// display. Raw HTML is escaped (WithUnsafe is off), so model output can't inject
// markup. Built once; Convert is safe for concurrent use.
var mdRenderer = goldmark.New(goldmark.WithExtensions(extension.GFM))

// renderMarkdown converts a markdown string to sanitized display HTML. On the
// rare convert error it falls back to escaped plaintext.
func renderMarkdown(s string) template.HTML {
	if s == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := mdRenderer.Convert([]byte(s), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(s))
	}
	return template.HTML(buf.String())
}
