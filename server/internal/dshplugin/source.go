// Package dshplugin resolves and validates DeepSeek Harness plugin packages.
//
// DSH ships no plugin registry of its own: `dsh plugin --profile <p> add <pkg>`
// is a pnpm passthrough executed in the profile directory, so a plugin is
// nothing more than an npm package whose manifest declares
// `dsh.bundle.patch`. This package therefore speaks npm's vocabulary rather
// than inventing a Multica-specific one, and its validation gates are the same
// ones the runtime adapter applies before booting a profile — so an import
// either fails here, with an explanation, or works in a task.
package dshplugin

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// SourceKind mirrors the source_kind column and the vocabulary the runtime
// adapter accepts in DSH_PLUGIN_SET.
type SourceKind string

const (
	SourceNPM    SourceKind = "npm"
	SourceGitHub SourceKind = "github"
	SourceURL    SourceKind = "url"
	SourceFile   SourceKind = "file"
)

// Source is a parsed package reference.
type Source struct {
	Kind SourceKind
	// Spec is the original text, stored verbatim so the runtime adapter
	// receives exactly what the operator typed.
	Spec string
	// Name is the npm package name for SourceNPM, else "".
	Name string
	// Version is the requested version for SourceNPM; "" means latest.
	Version string
	// Owner, Repo and Ref are set for SourceGitHub.
	Owner, Repo, Ref string
	// URL is the tarball URL for SourceURL.
	URL string
}

// npmNamePattern accepts both plain and scoped npm package names.
var npmNamePattern = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)

var githubSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidPackageName reports whether name is a usable npm package name.
func ValidPackageName(name string) bool {
	return len(name) <= 214 && npmNamePattern.MatchString(name)
}

// ParseSource reads one of the four spec shapes the runtime adapter accepts.
// A bare package name is treated as npm, because that is what an operator
// copies out of a plugin's install instructions.
func ParseSource(spec string) (Source, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Source{}, fmt.Errorf("source is required")
	}
	switch {
	case strings.HasPrefix(spec, "github:"):
		return parseGitHub(spec)
	case strings.HasPrefix(spec, "https://"):
		parsed, err := url.Parse(spec)
		if err != nil || parsed.Host == "" {
			return Source{}, fmt.Errorf("source is not a valid https URL")
		}
		return Source{Kind: SourceURL, Spec: spec, URL: spec}, nil
	case strings.HasPrefix(spec, "file:"):
		path := strings.TrimPrefix(spec, "file:")
		if !strings.HasPrefix(path, "/") {
			return Source{}, fmt.Errorf("a file: source must be an absolute path")
		}
		return Source{Kind: SourceFile, Spec: spec, URL: path}, nil
	case strings.HasPrefix(spec, "http://"):
		return Source{}, fmt.Errorf("a plain http source is not accepted; use https")
	}

	name, version := strings.TrimPrefix(spec, "npm:"), ""
	// A scoped name carries a leading @, so only split on an @ after index 0.
	if at := strings.LastIndex(name, "@"); at > 0 {
		name, version = name[:at], name[at+1:]
	}
	if !ValidPackageName(name) {
		return Source{}, fmt.Errorf("%q is not a valid npm package name", name)
	}
	return Source{
		Kind:    SourceNPM,
		Spec:    "npm:" + strings.TrimPrefix(spec, "npm:"),
		Name:    name,
		Version: version,
	}, nil
}

func parseGitHub(spec string) (Source, error) {
	rest := strings.TrimPrefix(spec, "github:")
	ref := ""
	if hash := strings.Index(rest, "#"); hash >= 0 {
		rest, ref = rest[:hash], rest[hash+1:]
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Source{}, fmt.Errorf("a github source looks like github:owner/repo#ref")
	}
	if !githubSegmentPattern.MatchString(parts[0]) || !githubSegmentPattern.MatchString(parts[1]) {
		return Source{}, fmt.Errorf("github owner and repo may only contain letters, digits, dot, dash and underscore")
	}
	if ref == "" {
		return Source{}, fmt.Errorf("a github source must pin a ref: github:owner/repo#<tag-or-sha>")
	}
	if !githubSegmentPattern.MatchString(ref) {
		return Source{}, fmt.Errorf("github ref may only contain letters, digits, dot, dash and underscore")
	}
	return Source{Kind: SourceGitHub, Spec: spec, Owner: parts[0], Repo: parts[1], Ref: ref}, nil
}

// TarballURL is where the package bytes come from. A GitHub source resolves to
// a codeload snapshot, which is a repository archive rather than a published
// package — Validate refuses one that still needs its build to run.
func (s Source) TarballURL(registry string) (string, error) {
	switch s.Kind {
	case SourceURL:
		return s.URL, nil
	case SourceGitHub:
		return fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/%s",
			url.PathEscape(s.Owner), url.PathEscape(s.Repo), url.PathEscape(s.Ref)), nil
	case SourceNPM:
		// Resolved separately: the registry names the tarball, so that the
		// dist digest it publishes is what gets pinned.
		return "", fmt.Errorf("an npm source resolves its tarball through the registry")
	}
	return "", fmt.Errorf("a %s source has no tarball URL", s.Kind)
}
