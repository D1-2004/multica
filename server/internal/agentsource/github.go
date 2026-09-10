package agentsource

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var githubRepositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ParseGitHubRepository accepts a repository slug or an HTTPS repository/branch
// link. A tree URL denotes a complete ref, never an inferred project directory.
// The caller must resolve that ref against GitHub before reading any content.
func ParseGitHubRepository(value, ref string) (string, string, error) {
	value = strings.TrimSpace(value)
	ref = strings.TrimSpace(ref)
	repository := value
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
			return "", "", errors.New("repository must be a GitHub HTTPS repository URL without credentials, query or fragment")
		}
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/"), "/"), "/")
		if len(parts) != 2 && (len(parts) < 4 || parts[2] != "tree") {
			return "", "", errors.New("repository URL must point to a repository or a branch")
		}
		repository = parts[0] + "/" + parts[1]
		if len(parts) >= 4 {
			linkRef := strings.Join(parts[3:], "/")
			if ref != "" && ref != linkRef {
				return "", "", errors.New("ref does not match the branch in the repository URL")
			}
			ref = linkRef
		}
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 {
		return "", "", errors.New("repository must have an owner and a name")
	}
	parts[1] = strings.TrimSuffix(parts[1], ".git")
	for _, part := range parts {
		if !githubRepositoryName.MatchString(part) || part == "." || part == ".." {
			return "", "", errors.New("invalid GitHub repository name")
		}
	}
	if ref != "" && !ValidGitRef(ref) {
		return "", "", errors.New("invalid Git ref")
	}
	return strings.Join(parts, "/"), ref, nil
}

func ValidGitRef(ref string) bool {
	if ref == "" || len(ref) > 255 || strings.HasPrefix(ref, "-") || strings.HasSuffix(ref, ".") ||
		strings.ContainsAny(ref, " ~^:?*[\\") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") ||
		strings.IndexFunc(ref, func(r rune) bool { return r < ' ' || r == '\u007f' }) >= 0 {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}
