package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/consumers"
	"github.com/spin-stack/go-tools/internal/gittest"
)

// repo is a git repository of a test's own: the one that is copied from, or a consumer's checkout.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) repo {
	t.Helper()
	r := repo{t, t.TempDir()}
	gittest.Run(t, r.dir, "init", "-q", "-b", "main")
	return r
}

// commit writes files and commits them, and answers the commit.
func (r repo) commit(files map[string]string) string {
	r.t.Helper()
	for name, content := range files {
		path := filepath.Join(r.dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
	gittest.Run(r.t, r.dir, "add", ".")
	gittest.Run(r.t, r.dir, "commit", "-q", "--allow-empty", "-m", "a change")
	return gittest.Run(r.t, r.dir, "rev-parse", "HEAD")
}

const faces = `- name: inter
  source: https://registry.npmjs.org/@fontsource/inter/-/inter-{version}.tgz
  version: 5.3.0
- name: jetbrains-mono
  source: https://registry.npmjs.org/@fontsource/jetbrains-mono/-/jetbrains-mono-{version}.tgz
  version: 5.3.0
- name: task
  source: https://github.com/go-task/task/releases/download/v{version}/task.tar.gz
  version: 3.54.0
`

const brandsFaces = `{"dependencies": {"@fontsource/inter": "5.3.0", "astro": "6.0.0"}, "devDependencies": {"@fontsource/jetbrains-mono": "5.3.0"}}`

func pin(commit, version, track string) string {
	return "- name: task\n  version: 3.54.0\n- name: brand\n  kind: git\n  version: " + version + "\n  pin: " + commit + "\n  track: " + track + "\n"
}

func table(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), consumers.File)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// asCheckout is the line that says a consumer was read from a checkout: a directory of the test's,
// which TestWhatCannotBeReadIsSaidAndTheRestKept holds.
var asCheckout = regexp.MustCompile(`\n  read as  /\S+ at HEAD`)

// reported is the report's sections by consumer, without asCheckout's lines, and its first line.
func reported(t *testing.T, args ...string) (head string, said map[string]string) {
	t.Helper()
	var out strings.Builder
	if err := run(t.Context(), args, &out); err != nil {
		t.Fatalf("a report failed on what it found: %v", err)
	}
	sections := strings.Split(strings.TrimSuffix(asCheckout.ReplaceAllString(out.String(), ""), "\n"), "\n\n")
	said = map[string]string{}
	for _, section := range sections[1:] {
		name, _, _ := strings.Cut(section, " ")
		said[name] = section
	}
	return sections[0], said
}

// TestEachConsumerIsReportedAsWhatItIs reads consumers from checkouts against a brand with two
// releases, the first two commits behind main.
func TestEachConsumerIsReportedAsWhatItIs(t *testing.T) {
	b := newRepo(t)
	first := b.commit(map[string]string{"dist/tokens.css": "old\n", "versions.yaml": faces})
	gittest.Run(t, b.dir, "tag", "v20261101.01")
	gittest.Run(t, b.dir, "tag", "screens-v20261019.02-c2f0602021cc")
	b.commit(map[string]string{"dist/tokens.css": "new\n"})
	head := b.commit(nil)
	gittest.Run(t, b.dir, "tag", "v20261102.01")

	checkouts := map[string]map[string]string{
		"current": {"versions.yaml": pin(head, "v20261102.01", "github-release"), "brand.css": "new\n", "package.json": brandsFaces},
		"behind":  {"versions.yaml": pin(first, "v20261101.01", "github-release"), "brand.css": "old\n", "package.json": brandsFaces},
		"edited":  {"versions.yaml": pin(head, "main", "branch"), "brand.css": "new\nand a line by hand\n", "package.json": brandsFaces},
		"faces": {"versions.yaml": pin(head, "main", "branch"), "brand.css": "new\n",
			"package.json": `{"dependencies": {"@fontsource/inter": "^5.3.0", "@fontsource/ibm-plex-mono": "5.3.0", "@fontsourcery/x": "1.0.0",
				"@fontsource-variable/inter": "^5.3.0", "fontsource": "1.0.0"}}`},
		"unpinned": {"brand.css": "a palette of its own\n", "package.json": brandsFaces},
		"other":    {"versions.yaml": "- name: task\n  version: 3.54.0\n- name: brand\n  version: main\n", "brand.css": "new\n", "package.json": brandsFaces},
		"copyless": {"versions.yaml": pin(head, "main", "branch"), "package.json": brandsFaces},
		"bare":     {"versions.yaml": pin(head, "main", "branch"), "brand.css": "new\n"},
	}
	content := "name = \"brand\"\nscopes = [\"@fontsource\", \"@fontsource-variable\"]\n\n"
	args := []string{"-dir", b.dir, "-main", "main"}
	for _, name := range []string{"current", "behind", "edited", "faces", "unpinned", "other", "copyless", "bare", "gone"} {
		content += "[[consumer]]\nname = \"" + name + "\"\nrepo = \"example/" + name + "\"\npin = \"versions.yaml\"\npackages = \"package.json\"\n" +
			"[consumer.copies]\n\"brand.css\" = \"dist/tokens.css\"\n\n"
		dir := filepath.Join(t.TempDir(), "absent")
		if files, ok := checkouts[name]; ok {
			c := newRepo(t)
			c.commit(files)
			dir = c.dir
		}
		args = append(args, "-checkout", "example/"+name+"="+dir+"@HEAD")
	}
	top, said := reported(t, append(args, "-table", table(t, content))...)
	if top != "brand main is "+head[:12] {
		t.Errorf("the report opens with %q", top)
	}

	const installed = "\n  packages brand's at HEAD: @fontsource/inter, @fontsource/jetbrains-mono"
	for consumer, want := range map[string]string{
		"current": "  pin      HEAD (v20261102.01, track github-release), the newest release v20261102.01; 0 commits behind main\n" +
			"  copies   all 1 are brand's at HEAD" + installed,
		"behind": "  pin      FIRST (v20261101.01, track github-release), release v20261101.01; the newest is v20261102.01; 2 commits behind main\n" +
			"  copies   all 1 are brand's at FIRST\n  packages brand's at FIRST: @fontsource/inter, @fontsource/jetbrains-mono",
		"edited": "  pin      HEAD (main, track branch), the newest release v20261102.01; 0 commits behind main\n" +
			"  copies   1 of 1 are not brand's at HEAD:\n    brand.css is not dist/tokens.css" + installed,
		"faces": "  pin      HEAD (main, track branch), the newest release v20261102.01; 0 commits behind main\n" +
			"  copies   all 1 are brand's at HEAD\n  packages not brand's at HEAD, which pins 2:\n" +
			"    @fontsource/inter is ^5.3.0, and brand's is 5.3.0\n    @fontsource/jetbrains-mono 5.3.0 is not installed\n" +
			"    @fontsource-variable/inter ^5.3.0 is no package brand pins\n    @fontsource/ibm-plex-mono 5.3.0 is no package brand pins",
		"unpinned": "  pin      none: it has no versions.yaml. Held to nothing, and compared with main\n" +
			"  copies   1 of 1 are not brand's at main:\n    brand.css is not dist/tokens.css\n" +
			"  packages brand's at main: @fontsource/inter, @fontsource/jetbrains-mono",
		"other": "  pin      none: its versions.yaml has no brand entry. Held to nothing, and compared with main\n" +
			"  copies   all 1 are brand's at main\n  packages brand's at main: @fontsource/inter, @fontsource/jetbrains-mono",
		"copyless": "  pin      HEAD (main, track branch), the newest release v20261102.01; 0 commits behind main\n" +
			"  copies   1 of 1 are not brand's at HEAD:\n    brand.css is not there (dist/tokens.css)" + installed,
		"bare": "  pin      HEAD (main, track branch), the newest release v20261102.01; 0 commits behind main\n" +
			"  copies   all 1 are brand's at HEAD\n  packages it has no package.json",
	} {
		want = consumer + "  example/" + consumer + "\n" + strings.NewReplacer("HEAD", head[:12], "FIRST", first[:12]).Replace(want)
		if said[consumer] != want {
			t.Errorf("%s is reported as:\n%s\nwant:\n%s", consumer, said[consumer], want)
		}
	}
	if gone := said["gone"]; !strings.HasPrefix(gone, "gone  example/gone\n  unread   ") || !strings.HasSuffix(gone, "absent has no HEAD") || strings.Count(gone, "\n") != 1 {
		t.Errorf("a consumer that cannot be read is reported as:\n%s", gone)
	}
}

// A consumer with no copies and no packages is its pin; a repository with no release, and a pin
// no release names, are said; and a table with no scope says nothing of a package not pinned.
func TestAPinIsPlacedAmongTheReleasesThereAre(t *testing.T) {
	b := newRepo(t)
	first := b.commit(map[string]string{"dist/tokens.css": "old\n", "versions.yaml": faces})
	c := newRepo(t)
	c.commit(map[string]string{"versions.yaml": pin(first, "main", "branch"),
		"package.json": `{"dependencies": {"@fontsource/inter": "5.3.0", "@fontsource/jetbrains-mono": "5.3.0", "@fontsource/ibm-plex-mono": "5.3.0"}}`})
	content := "name = \"brand\"\n[[consumer]]\nname = \"c\"\nrepo = \"example/c\"\npin = \"versions.yaml\"\n" +
		"[[consumer]]\nname = \"d\"\nrepo = \"example/c\"\npin = \"versions.yaml\"\npackages = \"package.json\"\n"
	args := []string{"-dir", b.dir, "-main", "main", "-checkout", "example/c=" + c.dir + "@HEAD", "-table", table(t, content)}

	_, said := reported(t, args...)
	if want := "c  example/c\n  pin      " + first[:12] + " (main, track branch), and brand has no release; 0 commits behind main\n  copies   none"; said["c"] != want {
		t.Errorf("with no release:\n%s\nwant:\n%s", said["c"], want)
	}
	if !strings.HasSuffix(said["d"], "\n  packages brand's at "+first[:12]+": @fontsource/inter, @fontsource/jetbrains-mono") {
		t.Errorf("with no scope, a package that is not pinned is said:\n%s", said["d"])
	}

	b.commit(nil)
	gittest.Run(t, b.dir, "tag", "v20261103.01")
	_, said = reported(t, args...)
	if want := "c  example/c\n  pin      " + first[:12] + " (main, track branch), which no release names; the newest is v20261103.01; 1 commits behind main\n  copies   none"; said["c"] != want {
		t.Errorf("with a release after the pin:\n%s\nwant:\n%s", said["c"], want)
	}
}

// A consumer pinned before versions.yaml named the faces is on a brand that pinned none: what it
// installs is said, and not as a difference from pins made later.
func TestPackagesAreHeldToThePinnedCommitNotToMain(t *testing.T) {
	b := newRepo(t)
	before := b.commit(map[string]string{"dist/tokens.css": "old\n", "versions.yaml": "- name: task\n  version: 3.54.0\n"})
	b.commit(map[string]string{"versions.yaml": faces})
	gittest.Run(t, b.dir, "rm", "-q", "versions.yaml")
	unpinned := b.commit(nil)
	c := newRepo(t)
	c.commit(map[string]string{"versions.yaml": pin(before, "main", "branch"), "package.json": `{"dependencies": {"@fontsource/ibm-plex-sans": "5.3.0"}}`})
	row := consumers.Consumer{Repo: "example/c", Pin: "versions.yaml", Packages: "package.json"}
	p := producer{"brand", b.dir, "main"}
	at := checkout{c.dir, "HEAD"}

	got := strings.Join(report(t.Context(), consumers.Table{Scopes: []string{"@fontsource"}}, row, at, p), "\n")
	if want := "\npackages brand pinned none at " + before[:12] + ", and it installs:\n  @fontsource/ibm-plex-sans 5.3.0 is no package brand pins"; !strings.HasSuffix(got, want) {
		t.Errorf("got:\n%s\nwant it to end:%s", got, want)
	}
	// At a commit with no versions.yaml at all, the same: nothing was pinned.
	c.commit(map[string]string{"versions.yaml": pin(unpinned, "main", "branch")})
	got = strings.Join(report(t.Context(), consumers.Table{Scopes: []string{"@fontsource"}}, row, at, p), "\n")
	if want := "\npackages brand pinned none at " + unpinned[:12] + ", and it installs:\n  @fontsource/ibm-plex-sans 5.3.0 is no package brand pins"; !strings.HasSuffix(got, want) {
		t.Errorf("got:\n%s\nwant it to end:%s", got, want)
	}
}

// What cannot be read as what it should be is said in the consumer's own section, and what was
// read before it is kept.
func TestWhatCannotBeReadIsSaidAndTheRestKept(t *testing.T) {
	b := newRepo(t)
	head := b.commit(map[string]string{"dist/tokens.css": "new\n", "versions.yaml": "- name: [\n"})
	stray := strings.Repeat("ab", 20)
	p := producer{"brand", b.dir, "main"}
	for name, tc := range map[string]struct {
		files map[string]string
		row   consumers.Consumer
		want  string
	}{
		"a pin the repository never had": {
			map[string]string{"versions.yaml": pin(stray, "main", "branch")},
			consumers.Consumer{Repo: "example/c", Pin: "versions.yaml"},
			"read as  DIR at HEAD\npin      " + stray + " (main), which is no commit of brand in this clone",
		},
		"a versions.yaml that is not one": {
			map[string]string{"versions.yaml": "- name: [\n"},
			consumers.Consumer{Repo: "example/c", Pin: "versions.yaml"},
			"read as  DIR at HEAD\nunread   versions.yaml: yaml: line 1: did not find expected node content",
		},
		"a package.json that is not one": {
			map[string]string{"versions.yaml": "- name: task\n", "package.json": "{"},
			consumers.Consumer{Repo: "example/c", Pin: "versions.yaml", Packages: "package.json"},
			"read as  DIR at HEAD\npin      none: its versions.yaml has no brand entry. Held to nothing, and compared with main\ncopies   none\n" +
				"unread   brand's versions.yaml at main: yaml: line 1: did not find expected node content",
		},
		"a file the repository had not": {
			map[string]string{"versions.yaml": pin(head, "main", "branch"), "a.css": "new\n", "b.css": "new\n"},
			consumers.Consumer{Repo: "example/c", Pin: "versions.yaml", Copies: map[string]string{"b.css": "dist/none.css", "a.css": "dist/tokens.css"}},
			"read as  DIR at HEAD\npin      " + head[:12] + " (main, track branch), and brand has no release; 0 commits behind main\n" +
				"copies   1 of 2 are not brand's at " + head[:12] + ":\n  b.css: brand had no dist/none.css then",
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := newRepo(t)
			c.commit(tc.files)
			got := strings.Join(report(t.Context(), consumers.Table{}, tc.row, checkout{c.dir, "HEAD"}, p), "\n")
			if want := strings.ReplaceAll(tc.want, "DIR", c.dir); got != want {
				t.Errorf("got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// gh is a `gh` on PATH that answers `gh api` as GitHub does for one repository, o/site, whose
// name GitHub now spells o/Site and whose only file is versions.yaml; any other repository is a
// 404, and o/down a failure that is not one.
func gh(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
case "$*" in
"api repos/o/site --jq .full_name") echo o/Site ;;
"api -H Accept: application/vnd.github.raw repos/o/site/contents/versions.yaml") printf -- '- name: task\n' ;;
*repos/o/down*) echo "gh: HTTP 502" >&2; exit 1 ;;
*) echo "gh: Not Found (HTTP 404)" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o700); err != nil { //nolint:gosec // a program the test runs
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// With no checkout given, a consumer is read through gh: by the name GitHub has for it, its
// files as they are, a file it has not as absent, and a repository this login cannot see as that.
func TestAConsumerIsReadThroughGH(t *testing.T) {
	gh(t)
	b := newRepo(t)
	b.commit(map[string]string{"dist/tokens.css": "new\n"})
	content := "name = \"brand\"\n" +
		"[[consumer]]\nname = \"site\"\nrepo = \"o/site\"\npin = \"versions.yaml\"\npackages = \"package.json\"\n[consumer.copies]\n\"brand.css\" = \"dist/tokens.css\"\n" +
		"[[consumer]]\nname = \"private\"\nrepo = \"o/private\"\npin = \"versions.yaml\"\n" +
		"[[consumer]]\nname = \"down\"\nrepo = \"o/down\"\npin = \"versions.yaml\"\n"
	_, said := reported(t, "-dir", b.dir, "-main", "main", "-table", table(t, content))
	for consumer, want := range map[string]string{
		"site": "site  o/site\n  read as  o/Site\n  pin      none: its versions.yaml has no brand entry. Held to nothing, and compared with main\n" +
			"  copies   1 of 1 are not brand's at main:\n    brand.css is not there (dist/tokens.css)\n  packages it has no package.json",
		"private": "private  o/private\n  unread   private or missing: GitHub answers 404 to this login",
		"down":    "down  o/down\n  unread   gh api: exit status 1: gh: HTTP 502",
	} {
		if said[consumer] != want {
			t.Errorf("%s is reported as:\n%s\nwant:\n%s", consumer, said[consumer], want)
		}
	}

	// A file GitHub fails to answer for is the consumer unread, not a file it lacks.
	if _, found, err := (github{"o/down"}).read(t.Context(), "versions.yaml"); err == nil || found {
		t.Errorf("a failure that is no 404 read as found=%t, %v", found, err)
	}
}

// The report needs its table, the ref it measures from, and flags it knows.
func TestNoTableNoRefOrAFlagThatIsNotOneIsNoReport(t *testing.T) {
	b := newRepo(t)
	b.commit(nil)
	good := table(t, "name = \"brand\"\n")
	for name, args := range map[string][]string{
		"no table":                {"-dir", b.dir, "-main", "main", "-table", filepath.Join(t.TempDir(), "none.toml")},
		"a ref the clone has not": {"-dir", b.dir, "-main", "origin/main", "-table", good},
		"a checkout with no dir":  {"-dir", b.dir, "-main", "main", "-table", good, "-checkout", "o/site"},
	} {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			if err := run(t.Context(), args, &out); err == nil || out.Len() != 0 {
				t.Errorf("got %v and %q", err, out.String())
			}
		})
	}
	// A checkout with no ref after an @ is read at origin/HEAD.
	_, said := reported(t, "-dir", b.dir, "-main", "main", "-checkout", "o/site="+b.dir,
		"-table", table(t, "name = \"brand\"\n[[consumer]]\nname = \"site\"\nrepo = \"o/site\"\npin = \"versions.yaml\"\n"))
	if want := "site  o/site\n  unread   " + b.dir + " has no origin/HEAD"; said["site"] != want {
		t.Errorf("got:\n%s\nwant:\n%s", said["site"], want)
	}
}

// A pin whose distance from main git cannot count is still its place, with what git said.
func TestAPinGitCannotCountFromIsStillPlaced(t *testing.T) {
	b := newRepo(t)
	head := b.commit(nil)
	got := producer{"brand", b.dir, "no-such-ref"}.where(t.Context(), pinned{Pin: head, Version: "main", Track: "branch"})
	if want := head[:12] + " (main, track branch), and brand has no release; git -C: exit status 128: fatal: "; !strings.HasPrefix(got, want) {
		t.Errorf("got %q, want it to start %q", got, want)
	}
}
