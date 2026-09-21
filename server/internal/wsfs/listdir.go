package wsfs

import (
	"sort"
	"strings"
)

// DirEntry is one child of a directory after a full readdir.
type DirEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size_bytes"`
	ModTime string `json:"modified_at,omitempty"`
}

// PageDirectory implements algorithm A: sort the whole directory by name,
// then slice. It never walks children. Directories larger than MaxDirectorySize
// fail instead of returning a misleading page.
func PageDirectory(entries []DirEntry, offset, limit int) ([]DirEntry, bool, int, error) {
	if len(entries) > MaxDirectorySize {
		return nil, false, 0, ErrDirectoryTooLarge
	}
	limit = ClampListLimit(limit)
	if offset < 0 {
		offset = 0
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.Compare(entries[i].Name, entries[j].Name) < 0
	})
	if offset >= len(entries) {
		return []DirEntry{}, false, offset, nil
	}
	end := offset + limit
	truncated := end < len(entries)
	if end > len(entries) {
		end = len(entries)
	}
	return entries[offset:end], truncated, end, nil
}
