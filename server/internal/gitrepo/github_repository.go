package gitrepo

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

type githubRemote struct {
	client *GitHubAppClient
	installation int64
	owner, name string
	metadata Repository
}

func (c *GitHubAppClient) Open(ctx context.Context, address Address, installation int64) (Remote, error) {
	if address.Provider != GitHub { return nil, errors.New("Git connection does not match repository host") }
	var metadata Repository
	if installation != 0 {
		repositories, err := c.ListRepositories(ctx, installation)
		if err != nil { return nil, err }
		for _, repository := range repositories { if strings.EqualFold(repository.FullName, address.Repository) { metadata = repository; break } }
		if metadata.FullName == "" { return nil, &APIError{StatusCode: http.StatusForbidden, Message: "repository is not accessible through this connection"} }
	} else {
		if err := c.doInstallationJSON(ctx, 0, http.MethodGet, "/repos/" + address.Repository, nil, &metadata); err != nil { return nil, err }
	}
	if metadata.FullName == "" || metadata.DefaultBranch == "" { return nil, errors.New("repository response is incomplete") }
	metadata.HTMLURL = "https://github.com/" + metadata.FullName
	parts := strings.Split(metadata.FullName, "/")
	if len(parts) != 2 || !strings.EqualFold(metadata.FullName,address.Repository) { return nil,errors.New("GitHub repository response identity does not match the requested repository") }
	return &githubRemote{client:c, installation:installation, owner:parts[0], name:parts[1], metadata:metadata}, nil
}

func (r *githubRemote) Info() Repository { return r.metadata }
func (r *githubRemote) GetTree(ctx context.Context, sha string) (Tree, error) { return r.client.GetTree(ctx,r.installation,r.owner,r.name,sha) }
func (r *githubRemote) GetBlob(ctx context.Context, sha string) ([]byte, error) { return r.client.GetBlob(ctx,r.installation,r.owner,r.name,sha) }
func (r *githubRemote) ResolveCommit(ctx context.Context, ref string) (string,error) { return r.client.ResolveCommit(ctx,r.installation,r.owner,r.name,ref) }
func (r *githubRemote) ListBranches(ctx context.Context) ([]Branch,error) { return r.client.ListBranches(ctx,r.installation,r.owner,r.name) }
func (r *githubRemote) ListTags(ctx context.Context) ([]Tag,error) { return r.client.ListTags(ctx,r.installation,r.owner,r.name) }

// PublicGitHub only performs anonymous requests. Workspace connections never
// fall back to this reader when an explicitly selected credential fails.
func PublicGitHub(httpClient *http.Client) *GitHubAppClient {
	base, _ := parseAPIBase(GitHubAPIBase)
	client := cloneHTTPClient(httpClient)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return errors.New("Git API redirects are not supported") }
	return &GitHubAppClient{baseURL:base, httpClient:client, responseLimit:defaultResponseLimit}
}
