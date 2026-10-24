package exposure_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/exposure"
)

// Each kind finds what is someone's and passes over what is no one's: AWS's documented example
// account, the domains reserved for examples and tests, a no-reply or ssh address, a systemd
// template's instance, and a public address.
func TestAPrivateValueIsFoundAndAnExampleIsNot(t *testing.T) {
	text := strings.Join([]string{
		"role arn:aws:iam::210987654321:role/deploy and arn:aws:iam::123456789012:role/example",
		`"account_id": "345678901234"`,
		"the subnet 10.42.0.0/16, the gateway 192.168.1.1, 172.20.3.4 and 172.32.0.1, and 8.8.8.8",
		"cp.spin.internal, printer.local, ip-10-0-1-2 and spin.dev",
		"ana@acme.io, ops@example.com, bot@e2e.test, git@github.com, ssh-ed25519@openssh.com, getty@tty2.service, 1+x@users.noreply.github.com, noreply@acme.io, no-reply@acme.io, git@acme.io",
	}, "\n")
	repo, _ := exposure.PrivateRepos("spin-stack", []string{"spin", "ci"})
	got := map[string][]string{}
	for _, m := range exposure.Scan(append(exposure.DefaultPatterns(), repo), text+"\nsee github.com/spin-stack/spin and github.com/spin-stack/go-tools") {
		got[m.Kind] = append(got[m.Kind], m.Value)
	}
	assert.Equal(t, map[string][]string{
		"aws-account":       {"210987654321", "345678901234"},
		"private-ip":        {"10.42.0.0", "192.168.1.1", "172.20.3.4"},
		"internal-host":     {"cp.spin.internal", "printer.local", "ip-10-0-1-2"},
		"email":             {"ana@acme.io"},
		"private-repo-link": {"github.com/spin-stack/spin"},
	}, got)
	_, ok := exposure.PrivateRepos("spin-stack", nil)
	assert.False(t, ok, "no private repository is no pattern")
}

// A repository's own kinds are `<kind> <regexp>` a line; a line that is not, or whose regexp
// does not compile, is refused with where it is.
func TestARepositorysOwnKindsAreRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patterns.txt")
	require.NoError(t, os.WriteFile(path, []byte("# what is ours\n\ncustomer (?i)acme corp\n"), 0o600))
	own, err := exposure.ReadPatterns(path)
	require.NoError(t, err)
	assert.Equal(t, []exposure.Match{{Kind: "customer", Value: "ACME Corp"}}, exposure.Scan(own, "a deal with ACME Corp"))

	require.NoError(t, os.WriteFile(path, []byte("customer\n"), 0o600))
	_, err = exposure.ReadPatterns(path)
	assert.ErrorContains(t, err, "patterns.txt:1")
	require.NoError(t, os.WriteFile(path, []byte("customer (\n"), 0o600))
	_, err = exposure.ReadPatterns(path)
	assert.ErrorContains(t, err, "patterns.txt:1")
}

// Every ref is read, not only the default branch: a value only a pull request's head reaches
// is found and said to be off the default branch, and one on it, on it. Authors' addresses are
// read, a deleted file is listed, and so is a binary one.
func TestEveryRefIsReadAndAPullRequestsHeadToo(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=Ana", "-c", "user.email=ana@acme.io", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	git("init", "-q")
	write("net.md", "the subnet is 10.42.0.0/16\n")
	write("old.md", "a draft\n")
	write("logo.png", "\x89PNG\x00\x01\x02")
	// A text line that ends as git's binary notice does is text, read for values.
	write("notes.md", "Binary files 10.9.8.7 differ\n")
	git("add", ".")
	git("commit", "-qm", "first")
	git("rm", "-q", "old.md")
	git("commit", "-qm", "drop the draft")
	// A pull request's head, as a mirror clone has it: never merged, public all the same.
	git("checkout", "-qb", "pr")
	write("deploy.md", "arn:aws:iam::210987654321:role/deploy\n")
	git("add", "deploy.md")
	git("commit", "-qm", "deploy")
	git("update-ref", "refs/pull/1/head", "HEAD")
	git("checkout", "-q", "main")
	git("branch", "-qD", "pr")

	h, err := exposure.ReadHistory(dir, exposure.DefaultPatterns())
	require.NoError(t, err)
	assert.Equal(t, 3, h.Commits)
	assert.Equal(t, 1, h.OffDefault)
	byKey := map[string]exposure.Finding{}
	for _, f := range h.Findings {
		byKey[f.Key()] = f
	}
	require.Contains(t, byKey, "aws-account 210987654321")
	assert.False(t, byKey["aws-account 210987654321"].OnDefault)
	assert.Contains(t, byKey["aws-account 210987654321"].First, "deploy.md")
	assert.Contains(t, byKey["aws-account 210987654321"].String(), "only off the default branch")
	require.Contains(t, byKey, "private-ip 10.9.8.7")
	require.Contains(t, byKey, "private-ip 10.42.0.0")
	assert.True(t, byKey["private-ip 10.42.0.0"].OnDefault)
	assert.Contains(t, byKey["private-ip 10.42.0.0"].String(), "on the default branch")
	require.Contains(t, byKey, "author-email ana@acme.io")
	// An author and a committer for each of the three commits.
	assert.Equal(t, 6, byKey["author-email ana@acme.io"].Count)
	assert.Equal(t, []string{"deploy.md", "old.md"}, h.Deleted, "a file only a ref off the default branch has is not on it either")
	assert.Equal(t, []string{"logo.png"}, h.Binaries)
}

// gitleaks' report becomes one finding a secret, keyed by its fingerprint and placed by its
// commit; a report that is not gitleaks' is an error.
func TestGitleaksReportIsReadAsFindings(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "report.json")
	require.NoError(t, os.WriteFile(report, []byte(`[{"RuleID":"aws-access-token","Commit":"abcdef0123456","File":"a.env","StartLine":3,"Fingerprint":"abcdef0123456:a.env:aws-access-token:3"}]`), 0o600))
	found, err := exposure.Secrets(report, func(c string) bool { return c == "abcdef0123456" })
	require.NoError(t, err)
	assert.Equal(t, []exposure.Finding{{Kind: "secret:aws-access-token", Value: "abcdef0123456:a.env:aws-access-token:3", Count: 1, First: "commit abcdef012 a.env:3", OnDefault: true}}, found)

	require.NoError(t, os.WriteFile(report, []byte(`{"not":"a list"}`), 0o600))
	_, err = exposure.Secrets(report, func(string) bool { return false })
	assert.Error(t, err)
	_, err = exposure.Secrets(filepath.Join(dir, "none.json"), func(string) bool { return false })
	assert.Error(t, err)
}

// The allowlist answers a value by its kind and value, or a whole kind with *; a line that
// answers nothing is stale.
func TestTheAllowlistAnswersAValueOrAKindAndGoesStale(t *testing.T) {
	found := []exposure.Finding{
		{Kind: "private-ip", Value: "10.42.0.0"}, {Kind: "private-ip", Value: "10.0.0.1"},
		{Kind: "internal-host", Value: "cp.spin.internal"}, {Kind: "email", Value: "ana@acme.io"},
	}
	unexplained, stale := exposure.Explain(map[string]bool{
		"private-ip *": true, "internal-host cp.spin.internal": true, "email bob@acme.io": true, "aws-account *": true,
	}, found)
	assert.Equal(t, []exposure.Finding{{Kind: "email", Value: "ana@acme.io"}}, unexplained)
	assert.Equal(t, []string{"aws-account *", "email bob@acme.io"}, stale)
}

// GitHub's side is read page by page: issues and pull requests, their comments and review
// comments, release notes; Actions' runs and artifacts are counted to be looked at, and the
// owner's private repositories, this one aside, are named.
func TestGitHubsSideIsReadEveryPage(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		switch r.URL.Path + "?" + r.URL.Query().Get("page") {
		case "/repos/o/r/issues?":
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/issues?page=2>; rel="next", <%s/repos/o/r/issues?page=2>; rel="last"`, srv.URL, srv.URL))
			_, _ = w.Write([]byte(`[{"number":1,"title":"down","body":"cp at 10.1.2.3"}]`))
		case "/repos/o/r/issues?2":
			_, _ = w.Write([]byte(`[{"number":2,"title":"fix","body":"by ana@acme.io","pull_request":{}}]`))
		case "/repos/o/r/issues/comments?":
			_, _ = w.Write([]byte(`[{"html_url":"https://github.com/o/r/issues/1#c1","body":"same 10.1.2.3"}]`))
		case "/repos/o/r/pulls/comments?":
			_, _ = w.Write([]byte(`[{"html_url":"https://github.com/o/r/pull/2#r1","body":"nothing here"}]`))
		case "/repos/o/r/releases?":
			_, _ = w.Write([]byte(`[{"tag_name":"v1","name":"v1","body":"deployed to db.corp","assets":[{"name":"a"},{"name":"b"}]}]`))
		case "/repos/o/r/actions/runs?":
			_, _ = w.Write([]byte(`{"total_count":7}`))
		case "/repos/o/r/actions/artifacts?":
			_, _ = w.Write([]byte(`{"total_count":3}`))
		case "/orgs/o/repos?":
			_, _ = w.Write([]byte(`[{"name":"r","private":true},{"name":"secret","private":true},{"name":"open","private":false}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	gh := exposure.GitHub{API: srv.URL, Token: "tok"}

	found, look, err := gh.Surfaces(t.Context(), "o/r", exposure.DefaultPatterns())
	require.NoError(t, err)
	assert.Equal(t, []exposure.Finding{
		{Kind: "private-ip", Value: "10.1.2.3", Count: 2, First: "issue #1", OnDefault: true},
		{Kind: "email", Value: "ana@acme.io", Count: 1, First: "pull request #2", OnDefault: true},
		{Kind: "internal-host", Value: "db.corp", Count: 1, First: "release v1", OnDefault: true},
	}, found)
	assert.Contains(t, look[0], "1 issue(s) and 1 pull request(s)")
	assert.Contains(t, look[1], "7 Actions run(s)")
	assert.Contains(t, look[2], "3 Actions artifact(s)")
	assert.Contains(t, look[3], "1 release(s) with 2 asset(s)")

	private, err := gh.PrivateRepos(t.Context(), "o", "r")
	require.NoError(t, err)
	assert.Equal(t, []string{"secret"}, private)

	_, _, err = exposure.GitHub{API: srv.URL, Token: "tok"}.Surfaces(t.Context(), "o/missing", nil)
	assert.ErrorContains(t, err, "404")
}
