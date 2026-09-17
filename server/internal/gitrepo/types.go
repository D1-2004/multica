package gitrepo

import "fmt"

type Repository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

type Branch struct {
	Name string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
	Protected bool `json:"protected"`
}

type Tag struct {
	Name string `json:"name"`
	Commit struct { SHA string `json:"sha"` } `json:"commit"`
}

type TreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

type Tree struct {
	SHA       string      `json:"sha"`
	Truncated bool        `json:"truncated"`
	Entries   []TreeEntry `json:"tree"`
}

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Git repository API returned status %d", e.StatusCode)
	}
	return fmt.Sprintf("Git repository API returned status %d: %s", e.StatusCode, e.Message)
}
