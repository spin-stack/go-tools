// Package consumers reads a repository's consumers.toml: who copies what the repository makes.
//
// A repository whose outputs are copied - brand's stylesheets and marks into the dashboard, the
// website and the blog - names every place that copies them in one table, and `go tool task
// consumers` (cmd/consumers) says which of its commits each is on. The repository's own tests
// read the table with Load to hold it to the tree: that every file a row copies is one it has.
//
//	name = "brand"            # what a consumer's versions.yaml calls this repository
//	scopes = ["@fontsource"]  # optional: the npm scopes whose packages are this repository's to pin
//
//	[[consumer]]
//	name = "website"          # what the report calls it
//	repo = "spin-stack/website"
//	pin = "versions.yaml"     # the file, there, whose entry `brand` names the commit it is on
//	packages = "package.json" # optional: the package.json held to the npm packages pinned here
//	[consumer.copies]
//	"src/styles/brand-tokens.css" = "dist/tokens.css"   # its file = the file here it is a copy of
package consumers

import (
	"fmt"
	"regexp"

	"github.com/BurntSushi/toml"
)

// File is the table's name at a repository's root.
const File = "consumers.toml"

// Table is a consumers.toml.
type Table struct {
	// Name is the repository's name as its consumers pin it: the entry of each one's versions.yaml.
	Name string `toml:"name"`
	// Scopes are the npm scopes - "@fontsource", "@fontsource-variable" - of which a consumer
	// installs only the packages this repository's versions.yaml pins; none holds a consumer to
	// the pinned packages and says nothing of any other. Each is a whole scope, the name before a
	// package's slash, and they are listed: a prefix would also be @fontsourcery's, and one scope
	// would not be the variable faces', which are the wrong ones a fork comes with.
	Scopes    []string   `toml:"scopes"`
	Consumers []Consumer `toml:"consumer"`
}

// Consumer is one place that copies from the repository.
type Consumer struct {
	Name     string            `toml:"name"`
	Repo     string            `toml:"repo"`
	Pin      string            `toml:"pin"`
	Packages string            `toml:"packages"`
	Copies   map[string]string `toml:"copies"`
}

var (
	repoName = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)
	scope    = regexp.MustCompile(`^@[a-z0-9][a-z0-9._~-]*$`)
)

// Load is the table at path. A field it does not know is refused, so a misspelt one is not a
// row silently held to less; so are a repository with no name, a consumer with no name or
// another's, a repo that is not owner/name, a consumer with no place for its pin, and a scope that
// is not one - "@fontsource/" or "fontsource" would hold no package, and say so of none.
func Load(path string) (Table, error) {
	var table Table
	md, err := toml.DecodeFile(path, &table)
	if err != nil {
		return Table{}, err
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		return Table{}, fmt.Errorf("%s: %s is no field of the table", path, extra[0])
	}
	if table.Name == "" {
		return Table{}, fmt.Errorf("%s: no name: what a consumer's versions.yaml calls this repository", path)
	}
	for _, s := range table.Scopes {
		if !scope.MatchString(s) {
			return Table{}, fmt.Errorf("%s: scope %q is not an npm scope, as @fontsource is", path, s)
		}
	}
	seen := map[string]bool{}
	for _, c := range table.Consumers {
		switch {
		case c.Name == "" || seen[c.Name]:
			return Table{}, fmt.Errorf("%s: a consumer's name is its own: %q", path, c.Name)
		case !repoName.MatchString(c.Repo):
			return Table{}, fmt.Errorf("%s: %s: repo %q is not owner/name", path, c.Name, c.Repo)
		case c.Pin == "":
			return Table{}, fmt.Errorf("%s: %s names no versions.yaml for its pin", path, c.Name)
		}
		seen[c.Name] = true
	}
	return table, nil
}
