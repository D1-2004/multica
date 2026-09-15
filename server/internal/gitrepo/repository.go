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
