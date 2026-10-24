// Command exposure says what a repository would show the world if it were made public, before
// anyone makes it so:
//
//	go tool exposure [-allow file] [-patterns file] [-gitleaks-config path] [-mirror dir] <owner/repo>
//
// It reads a mirror clone - every ref, a pull request's head included, which GitHub publishes
// with the repository and only GitHub can remove - with gitleaks for secrets and with patterns
// for what is private without being one (an AWS account, an address inside a network, an
// internal host name, a person's email, a link into a repository that stays private), and
// reads the repository's issues, pull requests, comments and releases on GitHub for the same.
// It fails on a finding the allowlist does not answer, and on an allowlist line that answers
// nothing; what only a person can judge - deleted files, binaries, Actions' logs and artifacts -
// it lists to look at.
package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/spin-stack/go-tools/internal/allow"
	"github.com/spin-stack/go-tools/internal/exposure"
	"github.com/spin-stack/go-tools/versions"
)

//go:embed versions.yaml
var pins []byte

func main() {
	allowPath := flag.String("allow", "", "values that are public on purpose, `<kind> <value>  # why` (or `<kind> *` for a whole kind) a line")
	patternsPath := flag.String("patterns", "", "this repository's own kinds, `<kind> <regexp>` a line: a customer's name, a code name")
	leaksConfig := flag.String("gitleaks-config", "", "the repository's gitleaks config, as a path in its tree at HEAD (hack/gitleaks.toml)")
	mirror := flag.String("mirror", "", "where the mirror clone is kept, cloned when absent and fetched when present; a temporary one by default")
	github := flag.Bool("github", true, "read the repository's issues, pull requests, comments and releases on GitHub")
	flag.Parse()
	if flag.NArg() != 1 || strings.Count(flag.Arg(0), "/") != 1 {
		fmt.Fprintln(os.Stderr, "usage: exposure [flags] <owner/repo>")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	status, err := run(ctx, os.Stdout, options{
		repo: flag.Arg(0), allow: *allowPath, patterns: *patternsPath, leaksConfig: *leaksConfig,
		mirror: *mirror, github: *github,
	})
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "exposure: %v\n", err)
		os.Exit(2)
	}
	os.Exit(status)
}

type options struct {
	repo, allow, patterns, leaksConfig, mirror string
	github                                     bool
}

func run(ctx context.Context, w io.Writer, o options) (int, error) {
	allowed, err := allow.Read(o.allow)
	if err != nil {
		return 0, err
	}
	patterns := exposure.DefaultPatterns()
	if o.patterns != "" {
		own, err := exposure.ReadPatterns(o.patterns)
		if err != nil {
			return 0, err
		}
		patterns = append(patterns, own...)
	}
	owner, name, _ := strings.Cut(o.repo, "/")
	var gh exposure.GitHub
	if o.github {
		gh = exposure.GitHub{API: "https://api.github.com", Token: token()}
		private, err := gh.PrivateRepos(ctx, owner, name)
		if err != nil {
			return 0, fmt.Errorf("listing %s's private repositories: %w", owner, err)
		}
		if p, ok := exposure.PrivateRepos(owner, private); ok {
			patterns = append(patterns, p)
		}
	}

	dir := o.mirror
	if dir == "" {
		tmp, err := os.MkdirTemp("", "exposure-")
		if err != nil {
			return 0, err
		}
		defer func() { _ = os.RemoveAll(tmp) }() // the clone this run made
		dir = filepath.Join(tmp, name+".git")
	}
	if err := mirrorClone(ctx, "https://github.com/"+o.repo+".git", dir); err != nil {
		return 0, err
	}

	history, err := exposure.ReadHistory(dir, patterns)
	if err != nil {
		return 0, err
	}
	findings := history.Findings
	leaks, version, err := secrets(ctx, dir, o.leaksConfig)
	if err != nil {
		return 0, err
	}
	findings = append(leaks, findings...)
	look := []string{
		fmt.Sprintf("%d file(s) some commit had and the default branch no longer does: deleted is not gone", len(history.Deleted)),
		fmt.Sprintf("%d binary file(s) some commit added: an image or an archive is read by opening it", len(history.Binaries)),
	}
	if o.github {
		surfaced, more, err := gh.Surfaces(ctx, o.repo, patterns)
		if err != nil {
			return 0, err
		}
		findings = append(findings, surfaced...)
		look = append(look, more...)
	}

	unexplained, stale := exposure.Explain(allowed, findings)
	_, _ = fmt.Fprintf(w, "exposure: %s: %d commit(s) in every ref, %d of them off the default branch; gitleaks %s over every ref found %d secret(s)\n",
		o.repo, history.Commits, history.OffDefault, version, len(leaks))
	for _, f := range unexplained {
		_, _ = fmt.Fprintf(w, "  %s\n", f)
	}
	for _, k := range stale {
		_, _ = fmt.Fprintf(w, "  %s is in %s and answers nothing any more: remove the line\n", k, o.allow)
	}
	_, _ = fmt.Fprintln(w, "To look at, by a person:")
	for _, l := range look {
		_, _ = fmt.Fprintf(w, "  - %s\n", l)
	}
	for _, f := range history.Deleted {
		_, _ = fmt.Fprintf(w, "      deleted: %s\n", f)
	}
	for _, f := range history.Binaries {
		_, _ = fmt.Fprintf(w, "      binary: %s\n", f)
	}
	if len(unexplained)+len(stale) > 0 {
		_, _ = fmt.Fprintf(w, "exposure: %d value(s) a public repository would show and nothing answers, %d stale allowlist line(s): rewrite or rotate what is private, or answer it with why it is public\n",
			len(unexplained), len(stale))
		return 1, nil
	}
	_, _ = fmt.Fprintf(w, "OK: every value found is answered (%d allowlist line(s)); a person still reads the list above before the switch.\n", len(allowed))
	return 0, nil
}

// mirrorClone makes dir a mirror of url - every ref, refs/pull/* included - or brings one up to
// date.
func mirrorClone(ctx context.Context, url, dir string) error {
	args := []string{"clone", "--mirror", "--quiet", url, dir}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		args = []string{"-C", dir, "remote", "update", "--prune"}
	}
	out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// secrets runs gitleaks, at the version versions.yaml beside this file pins, over every ref of
// the mirror at dir, with the repository's own config when one is named; its findings, and the
// version that found them.
func secrets(ctx context.Context, dir, config string) ([]exposure.Finding, string, error) {
	v, err := versions.Parse(pins)
	if err != nil {
		return nil, "", err
	}
	e, err := v.Get("gitleaks")
	if err != nil {
		return nil, "", err
	}
	tmp, err := os.MkdirTemp("", "exposure-gitleaks-")
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }() // this run's report
	report := filepath.Join(tmp, "report.json")
	args := []string{"run", e.Source + "@" + e.Version, "git", dir, "--log-opts=--all", "--no-banner", "--redact",
		"--report-format", "json", "--report-path", report, "--exit-code", "0"}
	if config != "" {
		// From the repository's own tree: a bare mirror has no working tree for gitleaks to find
		// it in, and the config the repository commits is the one its own check runs.
		raw, err := exec.CommandContext(ctx, "git", "-C", dir, "show", "HEAD:"+config).Output()
		if err != nil {
			return nil, "", fmt.Errorf("reading %s at HEAD: %w", config, err)
		}
		path := filepath.Join(tmp, "gitleaks.toml")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return nil, "", err
		}
		args = append(args, "--config", path)
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = tmp // outside any module: go run takes the version named, and no go.mod's
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, "", fmt.Errorf("gitleaks: %w: %s", err, strings.TrimSpace(string(out)))
	}
	onDefault, err := exposure.DefaultBranch(dir)
	if err != nil {
		return nil, "", err
	}
	found, err := exposure.Secrets(report, func(c string) bool { return onDefault[c] })
	return found, e.Version, err
}

// token is the GitHub token to read with: GITHUB_TOKEN, else gh's. A private repository's issues
// cannot be read without one.
func token() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	// No gh, or not logged in: anonymous, which reads a public repository and fails, saying so,
	// on a private one.
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
