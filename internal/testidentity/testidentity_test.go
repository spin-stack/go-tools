package testidentity_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/gittest"
	"github.com/spin-stack/go-tools/internal/testidentity"
)

// The package runs git, and a git of it that misses its repository - a mutant - finds the
// checkout's: fenced first, it finds none (internal/gittest).
func TestMain(m *testing.M) {
	gittest.Fence()
	os.Exit(m.Run())
}

// Which addresses are a test's, and the reason said for each: the reserved domains, an address
// the tree's tests spell, and nothing else - and never GitHub's own, even spelled in a test. The
// person here is made up: an author's real address spelled in this file would be a test's from
// then on.
func TestATestIdentityIsAReservedDomainOrAnAddressATestSpells(t *testing.T) {
	fixtures := map[string]string{
		"agent@spinbox.dev": "cmd/supervisor/sign_test.go",
		"335013441+spin-stack-release[bot]@users.noreply.github.com": "hack/machine/main_test.go",
		"noreply@github.com": "hack/machine/main_test.go",
	}
	for _, tc := range []struct {
		email string
		why   string // "" is not a test's
	}{
		{"ana@corp.example", ".example is reserved for testing"},
		{"Ana@CORP.EXAMPLE", ".example is reserved for testing"},
		{" ana@corp.example ", ".example is reserved for testing"},
		{"t@t", "a domain with no dot"},
		{"dev@localhost", "a domain with no dot"},
		{"no-address", "not an address"},
		{"t@example.com", "example.com is reserved"},
		{"t@example.com.", "example.com is reserved"},
		{"x@app.example.com", "example.com is reserved"},
		{"x@example.org", "example.org is reserved"},
		{"x@example.net", "example.net is reserved"},
		{"x@spin.test", ".test is reserved for testing"},
		{"gittest@go-tools.invalid", ".invalid is reserved for testing"},
		{"x@host.localhost", ".localhost is reserved for testing"},
		{"x@printer.local", ".local is reserved for testing"},
		{"x@router.home.arpa", "home.arpa is reserved"},
		{"x@home.arpa", "home.arpa is reserved"},
		{"x@host.internal", ".internal is reserved for testing"},
		{"agent@spinbox.dev", "spelled in cmd/supervisor/sign_test.go"},
		{"Agent@Spinbox.dev", "spelled in cmd/supervisor/sign_test.go"},
		{"person@gmail.com", ""},
		{"someone@spinbox.dev", ""},
		{"x@notexample.com", ""},
		{"x@example.community", ""},
		{"x@myhome.arpa", ""},
		{"x@testing.dev", ""},
		{"49699333+dependabot[bot]@users.noreply.github.com", ""},
		{"335013441+spin-stack-release[bot]@users.noreply.github.com", ""},
		{"noreply@github.com", ""},
	} {
		t.Run(tc.email, func(t *testing.T) {
			why := testidentity.Classify(tc.email, fixtures)
			if tc.why == "" {
				assert.Empty(t, why)
				return
			}
			assert.Contains(t, why, tc.why)
		})
	}
}

// A commit is held by its author and by its committer, and an allow line takes one address out,
// in any case; a line whose address is not a test's is itself reported.
func TestACommitByATestIdentityIsFoundUnlessAllowed(t *testing.T) {
	commits := []testidentity.Commit{
		{SHA: "746cd0df4", Subject: "the leak", AuthorEmail: "ana@corp.example", CommitterEmail: "person@gmail.com"},
		{SHA: "aaaa", Subject: "rebased by a test", AuthorEmail: "person@gmail.com", CommitterEmail: "t@t"},
		{SHA: "bbbb", Subject: "dependabot", AuthorEmail: "49699333+dependabot[bot]@users.noreply.github.com", CommitterEmail: "noreply@github.com"},
		{SHA: "cccc", Subject: "allowed", AuthorEmail: "bot@ci.example", CommitterEmail: "person@gmail.com"},
	}
	allow := map[string]bool{"Bot@CI.example": true, "person@gmail.com": true}
	var got []string
	for _, f := range testidentity.Check(commits, nil, allow) {
		got = append(got, f.Commit.SHA+" "+f.Role+" "+f.Email)
	}
	assert.Equal(t, []string{"746cd0df4 author ana@corp.example", "aaaa committer t@t"}, got)
	assert.Equal(t, []string{"person@gmail.com"}, testidentity.Stale(allow, nil))

	f := testidentity.Finding{Commit: commits[0], Role: "author", Email: "ana@corp.example", Why: "why"}
	assert.Equal(t, "746cd0df4 the leak: author ana@corp.example is a test identity (why)", f.String())
}

// The fixtures are read from the tree's tests and fixtures only - Go's, JavaScript's,
// Terraform's, and what a tests directory holds: an address in a document or the code is
// somebody's, not a test's.
func TestTheFixturesAreTheAddressesTheTestsSpell(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"cmd/supervisor/sign_test.go":            `"GIT_AUTHOR_EMAIL=agent@spinbox.dev"`,
		"configs/fixtures/e2e.yaml":              "email: user@e2e.test\n",
		"dashboard/e2e/admin.spec.ts":            "const a = 'Admin@Spin.Test'\n",
		"internal/x/testdata/fixture.txt":        "who: fixture@spinbox.dev\n",
		"internal/e2e/stack.go":                  "const owner = \"owner@e2e.dev\"\n",
		"src/auth.test.ts":                       "const u = 'unit@vscode.dev'\n",
		"test/e2e/cli.e2e.test.ts":               "const u = 'e2e@vscode.dev'\n",
		"test/mocks/vscode.ts":                   "const u = 'mock@vscode.dev'\n",
		"modules/cp/tests/posture.tftest.hcl":    "owner = \"tf@spinbox.dev\"\n",
		"lanes/stack.e2e.yaml":                   "owner: lane@spinbox.dev\n",
		"x/testutil/who.go":                      "// util@spinbox.dev\n",
		"web/__tests__/who.js":                   "// jest@spinbox.dev\n",
		"internal/x/testdata/again.txt":          "who: agent@spinbox.dev\n",
		"README.md":                              "write to person@gmail.com\n",
		"internal/x/x.go":                        "// owner@spinbox.dev\n",
		"src/testing.ts":                         "// code@spinbox.dev\n",
		"internal/attestation/attestation.go":    "// attest@spinbox.dev\n",
		"modules/cp/tests.tf":                    "# terraform@spinbox.dev\n",
		"internal/x/testdata/deleted-not-staged": "",
	}
	var names []string
	for name, body := range files {
		require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(body), 0o600))
		names = append(names, name)
	}
	require.NoError(t, os.Remove(filepath.Join(root, "internal/x/testdata/deleted-not-staged")))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "test", "submodule"), 0o700))
	// Sorted, so which file is first to spell an address is the same on every run.
	names = append(names, "test/submodule")
	slices.Sort(names)

	fixtures, err := testidentity.Fixtures(root, names)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"agent@spinbox.dev":   "cmd/supervisor/sign_test.go",
		"user@e2e.test":       "configs/fixtures/e2e.yaml",
		"admin@spin.test":     "dashboard/e2e/admin.spec.ts",
		"fixture@spinbox.dev": "internal/x/testdata/fixture.txt",
		"owner@e2e.dev":       "internal/e2e/stack.go",
		"unit@vscode.dev":     "src/auth.test.ts",
		"e2e@vscode.dev":      "test/e2e/cli.e2e.test.ts",
		"mock@vscode.dev":     "test/mocks/vscode.ts",
		"tf@spinbox.dev":      "modules/cp/tests/posture.tftest.hcl",
		"lane@spinbox.dev":    "lanes/stack.e2e.yaml",
		"util@spinbox.dev":    "x/testutil/who.go",
		"jest@spinbox.dev":    "web/__tests__/who.js",
	}, fixtures)
}

// The run, over a repository: its branch's commits since the base, the addresses its own tests
// spell, and an allow file; what it says for each answer, and its error for each.
func TestTheBranchsCommitsAreCheckedSinceTheBase(t *testing.T) {
	// A commit by name and email, committed by the same unless committer is set.
	type commit struct{ name, email, committer string }
	for _, tc := range []struct {
		name    string
		commits []commit
		allow   string
		base    string
		want    error
		says    []string
	}{
		{
			name:    "a person's commit",
			commits: []commit{{"Person", "person@gmail.com", ""}},
			says:    []string{"OK: 1 commit(s) since main, none by a test identity (1 addresses"},
		},
		{
			name:    "a commit by the checkout's test author",
			commits: []commit{{"Person", "person@gmail.com", ""}, {"Ana Lima", "ana@corp.example", ""}},
			want:    testidentity.ErrFound,
			says: []string{
				"the leak: author ana@corp.example is a test identity (.example is reserved",
				"the leak: committer ana@corp.example",
				"git config --show-origin --get-all user.email",
				"git rebase --exec 'git commit --amend --no-edit --reset-author' main",
			},
		},
		{
			name:    "a commit authored by an address the tree's test spells",
			commits: []commit{{"Fixture", "fixture@spinbox.dev", "person@gmail.com"}},
			want:    testidentity.ErrFound,
			says: []string{
				"the leak: author fixture@spinbox.dev is a test identity (spelled in x_test.go)",
				"git rebase --exec",
			},
		},
		{
			name:    "a commit committed by a test",
			commits: []commit{{"Person", "person@gmail.com", "t@t"}},
			want:    testidentity.ErrFound,
			says:    []string{"the leak: committer t@t is a test identity (a domain with no dot)"},
		},
		{
			name:    "an allowed address",
			commits: []commit{{"Bot", "bot@ci.example", ""}},
			allow:   "bot@ci.example  # the CI's own bot, which is real\n",
			says:    []string{"OK: 1 commit(s)"},
		},
		{
			name:    "an allow line for a real address",
			commits: []commit{{"Person", "person@gmail.com", ""}},
			allow:   "person@gmail.com  # not a test's\n",
			want:    testidentity.ErrFound,
			says:    []string{"person@gmail.com is not a test identity any more: take the line out"},
		},
		{
			name:  "an allow line with no reason",
			allow: "bot@ci.example\n",
			want:  errAny,
		},
		{
			name: "a base that is not here",
			base: "origin/main",
			want: errAny,
			says: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			gittest.Run(t, repo, "init", "-q", "-b", "main")
			require.NoError(t, os.WriteFile(filepath.Join(repo, "x_test.go"), []byte("// fixture@spinbox.dev\n"), 0o600))
			gittest.Run(t, repo, "add", ".")
			gittest.Run(t, repo, "commit", "-q", "-m", "base")
			gittest.Run(t, repo, "checkout", "-q", "-b", "branch")
			for i, c := range tc.commits {
				subject := "one"
				if i == len(tc.commits)-1 {
					subject = "the leak"
				}
				committer := c.email
				if c.committer != "" {
					committer = c.committer
				}
				cmd := gittest.Command(t, repo, "commit", "-q", "--allow-empty", "-m", subject)
				cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME="+c.name, "GIT_AUTHOR_EMAIL="+c.email,
					"GIT_COMMITTER_NAME="+c.name, "GIT_COMMITTER_EMAIL="+committer)
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
			}
			// A finding names its commit, by the first twelve of its name.
			head := gittest.Run(t, repo, "rev-parse", "HEAD")
			allowPath := ""
			if tc.allow != "" {
				allowPath = filepath.Join(t.TempDir(), "allow.txt")
				require.NoError(t, os.WriteFile(allowPath, []byte(tc.allow), 0o600))
			}
			base := "main"
			if tc.base != "" {
				base = tc.base
			}

			var out bytes.Buffer
			err := testidentity.Run(t.Context(), repo, base, allowPath, &out)
			switch tc.want {
			case nil:
				require.NoError(t, err, "%s", out.String())
			case errAny:
				require.Error(t, err)
				require.NotErrorIs(t, err, testidentity.ErrFound)
			default:
				require.ErrorIs(t, err, tc.want)
			}
			for _, s := range tc.says {
				if strings.HasPrefix(s, "the leak:") {
					s = head[:12] + " " + s
				}
				assert.Contains(t, out.String(), s)
			}
		})
	}
}

// errAny is a run that could not ask: anything but ErrFound.
var errAny = os.ErrInvalid
