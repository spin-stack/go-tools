// Package testidentity tells a commit's author from the identities a tree's tests use.
//
// Twice a test in spin set git's author on the checkout it ran in (`ana@corp.example`, 2026-10
// and 2026-11-12), and every worktree of it - agents' too - committed as that author, one commit
// onto main. A fence around a test's git keeps it off the checkout; this is the other side, the
// same in every repository: a branch whose commits say a test identity is not pushed as if
// somebody wrote them.
//
// A test identity is not a list kept by hand. It is an address at a domain reserved for
// documentation and testing - RFC 2606 and 6761 (.test, .example, .invalid, .localhost and
// example.com/.net/.org), 6762's .local, 8375's home.arpa, ICANN's .internal - or one with no
// dot at all (`t@t`); or an address spelled in one of the tree's tests or fixtures, read from
// the tree every run, so a fixture added tomorrow is refused tomorrow.
//
// GitHub's own addresses - an App's `<id>+<name>[bot]@users.noreply.github.com` (Dependabot, a
// release App), a person's noreply, and the noreply@github.com that commits a merge made on the
// site - are real by rule, before the tests are asked: GitHub mints them and writes them on what
// its Apps commit, and a test may spell one without making the App's own commits a test's.
package testidentity

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Commit is one commit's addresses, as git wrote them, before any .mailmap.
type Commit struct {
	SHA, Subject                string
	AuthorEmail, CommitterEmail string
}

// Finding is one identity of one commit that is a test's.
type Finding struct {
	Commit Commit
	Role   string // author or committer
	Email  string
	Why    string
}

func (f Finding) String() string {
	return fmt.Sprintf("%.12s %s: %s %s is a test identity (%s)", f.Commit.SHA, f.Commit.Subject, f.Role, f.Email, f.Why)
}

// reservedTLDs are the top-level names no registry delegates: RFC 2606 and 6761, 6762's .local,
// and .internal (ICANN, 2024).
var reservedTLDs = []string{"test", "example", "invalid", "localhost", "local", "internal"}

// reservedDomains are the names under a delegated top level that are reserved all the same:
// RFC 2606's three and RFC 8375's home.arpa.
var reservedDomains = []string{"example.com", "example.net", "example.org", "home.arpa"}

// Reserved says why domain is one no person's mail is at, or "" when it may be.
func Reserved(domain string) string {
	domain = strings.TrimSuffix(strings.ToLower(domain), ".")
	if !strings.Contains(domain, ".") {
		return "a domain with no dot"
	}
	if tld := domain[strings.LastIndex(domain, ".")+1:]; slices.Contains(reservedTLDs, tld) {
		return "." + tld + " is reserved for testing (RFC 2606, 6761, 6762)"
	}
	for _, d := range reservedDomains {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return d + " is reserved (RFC 2606, 8375)"
		}
	}
	return ""
}

// github is whether email is an address GitHub mints: a user's or an App's noreply, or the
// committer of what is merged on the site.
func github(email string) bool {
	return email == "noreply@github.com" || strings.HasSuffix(email, "@users.noreply.github.com")
}

// Classify says why email is a test identity, or "" when it is not. fixtures is every address
// the tree's tests and fixtures spell, and the file that spells it.
func Classify(email string, fixtures map[string]string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if github(email) {
		return ""
	}
	if file, ok := fixtures[email]; ok {
		return "spelled in " + file
	}
	_, domain, ok := strings.Cut(email, "@")
	if !ok {
		return "not an address"
	}
	return Reserved(domain)
}

// Check is every author and committer of commits that is a test identity, less what allow
// exempts (an address, any case).
func Check(commits []Commit, fixtures map[string]string, allow map[string]bool) []Finding {
	allowed := lower(allow)
	var found []Finding
	for _, c := range commits {
		for _, id := range []struct{ role, email string }{{"author", c.AuthorEmail}, {"committer", c.CommitterEmail}} {
			if allowed[strings.ToLower(id.email)] {
				continue
			}
			if why := Classify(id.email, fixtures); why != "" {
				found = append(found, Finding{Commit: c, Role: id.role, Email: id.email, Why: why})
			}
		}
	}
	return found
}

// Stale is every allowed address that is not a test identity any more: an exemption for nothing,
// which a ratchet does not keep. Whether a commit of the range uses it does not matter - the
// range is a branch's, and the address is somebody's on every branch.
func Stale(allow map[string]bool, fixtures map[string]string) []string {
	var stale []string
	for email := range allow {
		if Classify(email, fixtures) == "" {
			stale = append(stale, email)
		}
	}
	slices.Sort(stale)
	return stale
}

func lower(allow map[string]bool) map[string]bool {
	out := make(map[string]bool, len(allow))
	for k := range allow {
		out[strings.ToLower(k)] = true
	}
	return out
}

// address is an e-mail address as a test or fixture spells one.
var address = regexp.MustCompile(`[A-Za-z0-9._%+\[\]-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*`)

// fixtureDirs are the directories whose every file is a test's or a fixture: Go's testdata, and
// what the repositories here name theirs (test/unit, test/e2e, modules/x/tests, test/mocks).
var fixtureDirs = []string{"testdata", "fixtures", "testutil", "e2e", "test", "tests", "__tests__", "mocks"}

// Fixture is whether p, slash-separated and relative to the tree, is a test or a fixture: a Go
// test, a JavaScript or TypeScript one (x.test.ts, x.spec.mjs, x.e2e.test.ts), a Terraform test,
// an e2e configuration, or anything under a directory fixtureDirs names.
func Fixture(p string) bool {
	base := path.Base(p)
	if strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, ".tftest.hcl") || strings.HasSuffix(base, ".e2e.yaml") {
		return true
	}
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	return slices.ContainsFunc(strings.Split(path.Dir(p), "/"), func(dir string) bool {
		return slices.Contains(fixtureDirs, dir)
	})
}

// Fixtures is every address spelled in files under root that Fixture names, lower-case, with
// the first file that spells it. A path listed that is no file on disk - deleted and not yet
// staged, or a submodule's directory - is nothing to read.
func Fixtures(root string, files []string) (map[string]string, error) {
	found := map[string]string{}
	for _, f := range files {
		if !Fixture(f) {
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(f))
		if info, err := os.Stat(p); err != nil || !info.Mode().IsRegular() {
			continue
		}
		b, err := os.ReadFile(p) //nolint:gosec // a tracked file of the tree
		if err != nil {
			return nil, err
		}
		for _, m := range address.FindAll(b, -1) { // mutate-exempt: any negative count is every match
			if a := strings.ToLower(string(m)); found[a] == "" {
				found[a] = f
			}
		}
	}
	return found, nil
}
