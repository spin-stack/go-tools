// Package page renders the shop's pages.
package page

import (
	"io"
	"text/template"
)

var front = template.Must(template.New("front").Parse("<h1>{{.}}</h1>\n"))

// Front writes the front page.
func Front(w io.Writer, title string) error { return front.Execute(w, title) }
