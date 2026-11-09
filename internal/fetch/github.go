package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"strconv"
	"strings"
)

const githubAPI = "https://api.github.com/"

// token is the GitHub token the environment holds: GITHUB_TOKEN, which a workflow is given, or
// GH_TOKEN, which gh reads.
func token() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("GH_TOKEN")
}

// asset is the URL to fetch for url: with a token, a GitHub release download's asset through the
// API, and otherwise url. GitHub sheds anonymous downloads under load (2026-11-08 and -09:
// "increased timeouts due to surge in anonymous users", 504s for minutes at a time), and a token
// on the github.com URL itself does nothing - a private release's file answers 404 with one as
// without - so the asset is asked for where a token counts: GET /repos/{o}/{r}/releases/assets/{id}
// with Accept: application/octet-stream, which redirects to the same bytes.
func (p retry) asset(ctx context.Context, url string) (string, error) {
	owner, repo, tag, name, ok := releaseFile(url)
	if !ok || token() == "" {
		return url, nil
	}
	assets, err := p.release(ctx, owner, repo, tag)
	if err != nil {
		return "", fmt.Errorf("%s: %w", url, err)
	}
	id, ok := assets[name]
	if !ok {
		return "", fmt.Errorf("%s: release %s of %s/%s has no asset %s", url, tag, owner, repo, name)
	}
	return githubAPI + "repos/" + owner + "/" + repo + "/releases/assets/" + strconv.FormatInt(id, 10), nil
}

// release is the assets of owner/repo's release tag, by name; none when there is no such release.
func (p retry) release(ctx context.Context, owner, repo, tag string) (map[string]int64, error) {
	api := githubAPI + "repos/" + owner + "/" + repo + "/releases/tags/" + neturl.PathEscape(tag)
	var rel struct {
		Assets []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"assets"`
	}
	err := p.try(ctx, http.MethodGet, api, func(resp *http.Response, body io.Reader) error {
		switch resp.StatusCode {
		case http.StatusOK:
			return json.NewDecoder(body).Decode(&rel)
		case http.StatusNotFound:
			return nil
		}
		return answered(api, resp)
	})
	if err != nil {
		return nil, err
	}
	assets := make(map[string]int64, len(rel.Assets))
	for _, a := range rel.Assets {
		assets[a.Name] = a.ID
	}
	return assets, nil
}

// releaseFile is the release and the file a github.com release download names, and whether url is
// one: https://github.com/{owner}/{repo}/releases/download/{tag}/{name}.
func releaseFile(url string) (owner, repo, tag, name string, ok bool) {
	u, err := neturl.Parse(url)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.RawQuery != "" {
		return "", "", "", "", false
	}
	// A tag with a slash in it is a longer path, and downloads anonymously: none is pinned here.
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 6 || parts[2] != "releases" || parts[3] != "download" {
		return "", "", "", "", false
	}
	return parts[0], parts[1], parts[4], parts[5], true
}
