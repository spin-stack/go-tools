// Command notices writes an archive's THIRD_PARTY_NOTICES: the license, and the NOTICE an Apache
// module ships beside it, of everything compiled into the commands in it. The MIT, BSD and Apache
// licenses ask that these go with every copy, a binary one too.
//
//	go-licenses report --template internal/release/notices.tmpl ./cmd/... |
//	    go run ./internal/release/cmd/notices -go "$(go env GOVERSION)" -goroot "$(go env GOROOT)"
//
// The modules are go-licenses' lines; the Go standard library and runtime, which go-licenses
// leaves out and every binary carries, are GOROOT's LICENSE and PATENTS.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil { // mutate-exempt: the process's own arguments; run is what the tests drive
		fmt.Fprintln(os.Stderr, "notices:", err) //nolint:errcheck // the exit status says it
		os.Exit(1)                               // mutate-exempt: any status but 0 fails the step; run is what the tests drive
	}
}

const header = `The license and copyright notices of the third-party software compiled into the commands in
this archive, as each ships them. The commands themselves are under the Apache License 2.0
(https://github.com/spin-stack/go-tools/blob/main/LICENSE).
`

// module is one go-licenses library: the classifier may name more than one license in its file,
// and go-licenses gives it a line for each.
type module struct {
	name, version, file string
	licenses            []string
}

func run(args []string, r io.Reader, w io.Writer) error {
	fs := flag.NewFlagSet("notices", flag.ContinueOnError)
	goVersion := fs.String("go", "", "the Go the commands were built with (go env GOVERSION)")
	goroot := fs.String("goroot", "", "its GOROOT (go env GOROOT)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *goVersion == "" || *goroot == "" || fs.NArg() != 0 {
		return errors.New("notices -go <version> -goroot <dir> < go-licenses report")
	}
	mods, err := parse(r)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(header)
	goLicense, err := read(filepath.Join(*goroot, "LICENSE"), filepath.Join(*goroot, "PATENTS"))
	if err != nil {
		return err
	}
	entry(&b, "Go standard library and runtime "+*goVersion+" (BSD-3-Clause)", goLicense)
	for _, m := range mods {
		texts, err := read(append([]string{m.file}, notices(filepath.Dir(m.file))...)...)
		if err != nil {
			return fmt.Errorf("%s: %w", m.name, err)
		}
		entry(&b, fmt.Sprintf("%s %s (%s)", m.name, m.version, strings.Join(m.licenses, ", ")), texts)
	}
	_, err = io.WriteString(w, b.String())
	return err
}

// parse is go-licenses' lines as modules, sorted by name. A module whose license go-licenses could
// not find or name is an error: it is one this archive could not carry the notice of.
func parse(r io.Reader) ([]module, error) {
	byName := map[string]*module{}
	s := bufio.NewScanner(r)
	for s.Scan() {
		if s.Text() == "" {
			continue
		}
		f := strings.Split(s.Text(), "\t")
		if len(f) != 4 {
			return nil, fmt.Errorf("not name, version, license and file: %q", s.Text())
		}
		if f[2] == "Unknown" || f[3] == "Unknown" || f[3] == "" {
			return nil, fmt.Errorf("%s: go-licenses found no license it can name", f[0])
		}
		m := byName[f[0]]
		if m == nil {
			m = &module{name: f[0], version: f[1], file: f[3]}
			byName[f[0]] = m
		}
		m.licenses = append(m.licenses, f[2])
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	mods := make([]module, 0, len(byName))
	for _, m := range byName {
		mods = append(mods, *m)
	}
	slices.SortFunc(mods, func(a, b module) int { return strings.Compare(a.name, b.name) })
	return mods, nil
}

// notices is the NOTICE files in dir, the directory a module's license is in: what Apache-2.0's
// section 4(d) asks a redistribution to carry along with the license.
func notices(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // the license read beside it says what is wrong with dir
	}
	var out []string
	for _, e := range entries {
		switch strings.ToLower(e.Name()) {
		case "notice", "notice.txt", "notice.md":
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func read(files ...string) (string, error) {
	var b strings.Builder
	for i, f := range files {
		data, err := os.ReadFile(f) //nolint:gosec // a license file go-licenses named, or GOROOT's
		if err != nil {
			return "", err
		}
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimRight(string(data), "\n") + "\n")
	}
	return b.String(), nil
}

func entry(b *strings.Builder, title, text string) {
	rule := strings.Repeat("=", 80)
	fmt.Fprintf(b, "\n%s\n%s\n%s\n\n%s", rule, title, rule, text)
}
