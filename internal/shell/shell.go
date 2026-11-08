// Package shell reads the shell a repository's files run as the lines a shell runs them: what sfw
// and taskgate judge, line by line.
package shell

import "strings"

// Line is one line of shell, with the line of the file it starts on.
type Line struct {
	Text string
	Line int
}

// Lines is text as the lines a shell runs, the first of them on line first of its file: a line
// continued with a backslash joined to the next, a comment and a blank line dropped.
func Lines(text string, first int) []Line {
	var out []Line
	var cur strings.Builder
	start := first
	for i, l := range strings.Split(text, "\n") {
		if cur.Len() == 0 {
			start = first + i
		}
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		if strings.HasSuffix(l, "\\") {
			cur.WriteString(strings.TrimSuffix(l, "\\") + " ")
			continue
		}
		cur.WriteString(l)
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, Line{s, start})
		}
		cur.Reset()
	}
	return out
}

// Words is a line's words as a shell splits them: on blanks outside quotes, each word with its
// quotes taken off. What a shell expands - $VAR, $(...) - stays as written; a caller that cares
// knows a word by its $.
func Words(line string) []string {
	var out []string
	var cur strings.Builder
	word, quote := false, rune(0)
	escaped := false
	for _, r := range line {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, word = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, word = r, true
		case r == ' ' || r == '\t':
			if word {
				out = append(out, cur.String())
				cur.Reset()
				word = false
			}
		default:
			cur.WriteRune(r)
			word = true
		}
	}
	if word {
		out = append(out, cur.String())
	}
	return out
}
