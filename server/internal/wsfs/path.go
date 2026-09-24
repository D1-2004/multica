package wsfs

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	DefaultListLimit = 200
	MaxListLimit     = 1000
	MaxDirectorySize = 10000
	MaxNameBytes     = 255
	MaxPathBytes     = 4096
)

var (
	ErrInvalidPath       = errors.New("filesystem_invalid_path")
	ErrDirectoryTooLarge = errors.New("filesystem_directory_too_large")
)

// JailRelPath returns a cleaned relative path inside the files jail.
// The HTTP/POSIX jail root is the shared AgenticSpace root or the
// employee $MULTICA_FS_ROOT/files directory. Empty path means the jail root.
func JailRelPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "/" {
		return ".", nil
	}
	path = strings.ReplaceAll(path, "\\", "/")
	if strings.ContainsRune(path, 0) || !utf8.ValidString(path) || len(path) > MaxPathBytes {
		return "", ErrInvalidPath
	}
	var parts []string
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || strings.Contains(part, "\\") || len(part) > MaxNameBytes {
			return "", ErrInvalidPath
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return ".", nil
	}
	return strings.Join(parts, "/"), nil
}

func JailFileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || !utf8.ValidString(name) || len(name) > MaxNameBytes {
		return "", ErrInvalidPath
	}
	return name, nil
}

func ClampListLimit(limit int) int {
	if limit <= 0 {
		return DefaultListLimit
	}
	if limit > MaxListLimit {
		return MaxListLimit
	}
	return limit
}
