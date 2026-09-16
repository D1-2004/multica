package gitrepo

import "context"

// Reader reads immutable Git objects without exposing provider credentials.
type Reader interface {
	GetTree(context.Context, string) (Tree, error)
	GetBlob(context.Context, string) ([]byte, error)
}

type Remote interface {
	Reader
	Info() Repository
	ResolveCommit(context.Context, string) (string, error)
	ListBranches(context.Context) ([]Branch, error)
	ListTags(context.Context) ([]Tag, error)
}

func IsCommitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
