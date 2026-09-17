package gitrepo

import "strings"

func ValidRef(ref string) bool {
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
