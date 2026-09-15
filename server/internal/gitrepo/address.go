package gitrepo

import (
	"errors"
	"net/url"
	"strings"
)

const GitHub = "github"
const AlibabaCode = "alibaba_code"

// Address contains only canonical public repository metadata, never credentials.
type Address struct {
	Provider string
	Repository string
	URL string
	LinkKind string
	LinkPath string
}

func ParseAddress(raw string) (Address, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "git@") && !strings.Contains(raw, "://") {
		raw = "ssh://" + strings.Replace(raw, ":", "/", 1)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Port() != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return Address{}, errors.New("enter a Git repository URL without credentials, query or fragment")
	}
	if u.Scheme != "https" && u.Scheme != "ssh" { return Address{}, errors.New("repository URL must use HTTPS or SSH") }
	if u.User != nil && (u.Scheme != "ssh" || u.User.String() != "git") { return Address{}, errors.New("repository URL must not contain credentials") }
	var result Address
	var host string
	switch strings.ToLower(u.Hostname()) {
	case "github.com", "www.github.com": result.Provider, host = GitHub, "github.com"
	case "code.alibaba-inc.com", "gitlab.alibaba-inc.com", "code.aone.alibaba-inc.com", "code-sc.aone.alibaba-inc.com": result.Provider, host = AlibabaCode, "code.alibaba-inc.com"
	default: return Address{}, errors.New("this Git repository host is not supported")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n\t ?#%") { return Address{}, errors.New("invalid repository path") }
	}
	repoEnd := len(parts)
	for i := 2; i < len(parts); i++ {
		if parts[i] == "tree" || parts[i] == "blob" || parts[i] == "commit" {
			repoEnd = i
			if parts[i-1] == "-" { repoEnd-- }
			result.LinkKind, result.LinkPath = parts[i], strings.Join(parts[i+1:], "/")
			if result.LinkPath == "" { return Address{}, errors.New("repository revision is missing") }
			break
		}
	}
	if repoEnd < 2 || (result.Provider == GitHub && repoEnd != 2) { return Address{}, errors.New("repository URL must identify its namespace and repository") }
	parts[repoEnd-1] = strings.TrimSuffix(parts[repoEnd-1], ".git")
	if parts[repoEnd-1] == "" { return Address{}, errors.New("repository name is missing") }
	result.Repository = strings.Join(parts[:repoEnd], "/")
	result.URL = (&url.URL{Scheme:"https",Host:host,Path:"/"+result.Repository}).String()
	return result, nil
}
