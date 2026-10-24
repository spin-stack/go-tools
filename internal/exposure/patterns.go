// Package exposure says what a repository would show the world if it were made public: every
// commit any ref reaches (a pull request's head included, which GitHub publishes with the
// repository and which only GitHub can remove), and the issues, pull requests, comments and
// releases around it. A secret is gitleaks' to find; this finds what is private without being
// a secret - an account id, an address inside a network, a host's internal name, a person's
// email, a link into a repository that stays private - and lists what only a person can judge.
package exposure

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

// Pattern is one kind of private value: the regexp finds it, and its first group, when it has
// one, is the value (an account id inside an ARN); otherwise the whole match is.
type Pattern struct {
	Kind string
	Re   *regexp.Regexp
	// Skip is the values that are no one's: AWS's documented example account, an address
	// reserved for documentation.
	Skip func(string) bool
}

// examples are the values that documentation uses because they belong to no one.
var examples = map[string]bool{
	"123456789012": true, "000000000000": true, "111111111111": true, "111122223333": true,
	"444455556666": true, "777788889999": true,
}

// DefaultPatterns are the kinds every repository is read for.
func DefaultPatterns() []Pattern {
	return []Pattern{
		{
			Kind: "aws-account",
			Re:   regexp.MustCompile(`(?:arn:aws[\w-]*:[\w-]*:[\w-]*:|(?i:account[_ -]?id)["':= ]{1,4})(\d{12})\b`),
			Skip: func(v string) bool { return examples[v] },
		},
		{
			Kind: "private-ip",
			Re:   regexp.MustCompile(`\b(?:10\.\d{1,3}|192\.168|172\.(?:1[6-9]|2\d|3[01]))\.\d{1,3}\.\d{1,3}\b`),
		},
		{
			// The whole name, judged by its last label: gateway.internal.example is an example's,
			// and a regexp that stopped at .internal would call it a host inside a network.
			Kind: "internal-host",
			Re:   regexp.MustCompile(`\b(?:[a-z0-9-]+\.)+[a-z0-9-]+\b`),
			Skip: func(name string) bool {
				return !slices.Contains([]string{"internal", "local", "corp", "lan", "intranet"}, name[strings.LastIndex(name, ".")+1:])
			},
		},
		{
			Kind: "internal-host",
			Re:   regexp.MustCompile(`\bip-\d{1,3}-\d{1,3}-\d{1,3}-\d{1,3}\b`),
		},
		{
			Kind: "email",
			Re:   regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}\b`),
			Skip: anonymous,
		},
	}
}

// anonymous is an address that names nobody: the domains RFC 2606 and RFC 6761 reserve for
// examples and tests, GitHub's no-reply addresses, a forge's ssh user (git@), the names of SSH's
// extensions (…@openssh.com), and a systemd template's instance (getty@tty2.service), which has
// an address's shape and no one behind it.
func anonymous(addr string) bool {
	addr = strings.ToLower(addr)
	local, domain, ok := strings.Cut(addr, "@")
	if !ok {
		return true // no address at all: git takes an empty one
	}
	for _, d := range []string{"example.com", "example.org", "example.net", "users.noreply.github.com", "noreply.github.com", "openssh.com"} {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	for _, tld := range []string{".test", ".invalid", ".localhost", ".example", ".service", ".socket", ".target", ".mount", ".timer"} {
		if strings.HasSuffix(domain, tld) {
			return true
		}
	}
	return slices.Contains([]string{"git", "noreply", "no-reply"}, local)
}

// PrivateRepos is the pattern for links into repositories that stay private when this one is
// public: each 404s for a reader, and its name says what exists.
func PrivateRepos(owner string, repos []string) (Pattern, bool) {
	if len(repos) == 0 {
		return Pattern{}, false
	}
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = regexp.QuoteMeta(r)
	}
	re := fmt.Sprintf(`github\.com/%s/(?:%s)\b`, regexp.QuoteMeta(owner), strings.Join(names, "|"))
	return Pattern{Kind: "private-repo-link", Re: regexp.MustCompile(re)}, true
}

// ReadPatterns reads a repository's own kinds, one `<kind> <regexp>` a line: a customer's name,
// a project's code name. A blank line and a # comment are neither.
func ReadPatterns(path string) ([]Pattern, error) {
	f, err := os.Open(path) //nolint:gosec // the patterns file the caller names
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only
	var out []Pattern
	s := bufio.NewScanner(f)
	for n := 1; s.Scan(); n++ {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kind, expr, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("%s:%d: a pattern is `<kind> <regexp>`", path, n)
		}
		re, err := regexp.Compile(strings.TrimSpace(expr))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, Pattern{Kind: kind, Re: re})
	}
	return out, s.Err()
}

// Match is one private value found in a text.
type Match struct {
	Kind  string
	Value string
}

// Scan is every private value in text, in the order found.
func Scan(patterns []Pattern, text string) []Match {
	var out []Match
	for _, p := range patterns {
		for _, m := range p.Re.FindAllStringSubmatch(text, -1) { // mutate-exempt: any n < 0 is every match
			// The first group that matched is the value; with none, the whole match is.
			v := m[0]
			for _, g := range m[1:] {
				if g != "" {
					v = g
					break
				}
			}
			if p.Skip != nil && p.Skip(v) {
				continue
			}
			out = append(out, Match{Kind: p.Kind, Value: v})
		}
	}
	return out
}
