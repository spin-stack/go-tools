package gencache

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// directive is one //go:generate line of a package's file, as go generate would read it.
type directive struct {
	pkg  *pkg
	file string // the file's name, in pkg.Dir
	line int
	text string // the line as written, which `go generate -run` matches
	// words are the line split and expanded as go generate does; nil when it could not be.
	words []string
	// aliases are the file's -command lines. `go generate -run` skips every line it does not
	// match, a -command line too, so running one directive of the file means matching them.
	aliases []string
}

func (d directive) String() string {
	return fmt.Sprintf("%s:%d: %s", filepath.Join(d.pkg.Dir, d.file), d.line, strings.TrimPrefix(d.text, "//go:generate "))
}

// directives reads a file's //go:generate lines. A file that defines a -command alias is read
// with no directive's words, since what they mean is the alias's.
func directives(p *pkg, file, pkgName string, env map[string]string) ([]directive, error) {
	b, err := os.ReadFile(filepath.Join(p.Dir, file))
	if err != nil {
		return nil, err
	}
	var out []directive
	var aliases []string
	for i, text := range strings.Split(string(b), "\n") {
		text = strings.TrimRight(text, " \t\r")
		if !strings.HasPrefix(text, "//go:generate ") && !strings.HasPrefix(text, "//go:generate\t") {
			continue
		}
		words, err := split(text[len("//go:generate "):])
		if err != nil {
			words = nil
		}
		// mutate-exempt: a -command line names its alias, so it has more than one word; go generate refuses one that does not
		if len(words) > 0 && words[0] == "-command" {
			aliases = append(aliases, text)
			continue
		}
		n := i + 1
		for j, w := range words {
			words[j] = os.Expand(w, func(name string) string {
				switch name {
				case "GOFILE":
					return file
				case "GOLINE":
					return strconv.Itoa(n)
				case "GOPACKAGE":
					return pkgName
				case "DOLLAR":
					return "$"
				}
				if v, ok := env[name]; ok {
					return v
				}
				return os.Getenv(name)
			})
		}
		out = append(out, directive{pkg: p, file: file, line: n, text: text, words: words})
	}
	if len(aliases) > 0 {
		for i := range out {
			out[i].words, out[i].aliases = nil, aliases
		}
	}
	return out, nil
}

// split is go generate's: words separated by spaces or tabs, a word in double quotes as Go
// unquotes it.
func split(line string) ([]string, error) {
	var words []string
	for {
		line = strings.TrimLeft(line, " \t")
		if line == "" {
			return words, nil
		}
		if line[0] != '"' {
			i := strings.IndexAny(line, " \t")
			if i < 0 { // mutate-exempt: i is never 0, since the line starts with neither
				i = len(line)
			}
			words = append(words, line[:i])
			line = line[i:]
			continue
		}
		i := 1
		for i < len(line) && line[i] != '"' {
			if line[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(line) {
			return nil, errors.New("mismatched quoted string")
		}
		w, err := strconv.Unquote(line[:i+1])
		if err != nil {
			return nil, err
		}
		words = append(words, w)
		line = line[i+1:]
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			return nil, errors.New("expect space after quoted argument")
		}
	}
}

// mockgen is what a `go tool mockgen` directive asks for, where its output depends only on what
// the key covers.
type mockgen struct {
	tool        string // the name `go tool` was given
	destination string // relative to the package's directory, or absolute
	source      string // source mode's file; "" in package mode
	pkgPath     string // package mode's package
}

// readsMore are mockgen's flags that make its output depend on something the key does not
// cover: another file, the build's flags, or a model instead of the code.
var readsMore = []string{"archive", "aux_files", "build_flags", "copyright_file", "model_gob", "exec_only"}

// parseMockgen says what a directive's mockgen reads, or why it cannot be keyed. Its flags are
// mockgen's own (go.uber.org/mock), so one it does not know is a reason rather than a guess.
func parseMockgen(words []string) (mockgen, string) {
	if len(words) < 3 || words[0] != "go" || words[1] != "tool" ||
		(words[2] != "mockgen" && !strings.HasSuffix(words[2], "/mockgen")) {
		return mockgen{}, "not go tool mockgen"
	}
	fs := flag.NewFlagSet("mockgen", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var m mockgen
	m.tool = words[2]
	strs := map[string]*string{}
	for _, name := range []string{"source", "destination", "mock_names", "package", "self_package", "build_constraint",
		"imports", "exclude_interfaces", "archive", "aux_files", "build_flags", "copyright_file", "model_gob", "exec_only"} {
		strs[name] = fs.String(name, "", "")
	}
	for _, name := range []string{"write_command_comment", "write_package_comment", "write_source_comment", "write_generate_directive", "typed"} {
		fs.Bool(name, false, "")
	}
	version := fs.Bool("version", false, "")
	debug := fs.Bool("debug_parser", false, "")
	progOnly := fs.Bool("prog_only", false, "")
	if err := fs.Parse(words[3:]); err != nil {
		return mockgen{}, "a flag mockgen v0.6 does not have"
	}
	for _, name := range readsMore {
		if *strs[name] != "" {
			return mockgen{}, "-" + name
		}
	}
	if *version || *debug || *progOnly {
		return mockgen{}, "writes no mock"
	}
	m.destination, m.source = *strs["destination"], *strs["source"]
	if m.destination == "" {
		return mockgen{}, "no -destination"
	}
	// Source mode reads no argument, so any is as good as none.
	switch {
	case m.source != "":
	case fs.NArg() == 2:
		m.pkgPath = fs.Arg(0)
	default:
		return mockgen{}, "arguments mockgen would refuse"
	}
	return m, ""
}
