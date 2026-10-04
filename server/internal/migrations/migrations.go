package migrations

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/selfexec"
)

const maxSearchDepth = 4

var candidateLeaves = []string{
	"migrations",
	filepath.Join("server", "migrations"),
}

// ResolveDir returns the first migrations directory that exists from the
// current working directory.
func ResolveDir() (string, error) {
	seen := make(map[string]bool)
	for _, root := range searchRoots() {
		base := root
		for range maxSearchDepth + 1 {
			for _, leaf := range candidateLeaves {
				dir := filepath.Clean(filepath.Join(base, leaf))
				if seen[dir] {
					continue
				}
				seen[dir] = true
				info, err := os.Stat(dir)
				if err == nil && info.IsDir() {
					return dir, nil
				}
			}
			base = filepath.Join(base, "..")
		}
	}
	return "", fmt.Errorf("migrations directory not found")
}

func searchRoots() []string {
	roots := []string{"."}
	if exe, err := selfexec.Resolve(); err == nil {
		roots = append(roots, filepath.Dir(exe))
	}
	return roots
}

// Files returns sorted migration files for the given direction ("up" or
// "down").
func Files(direction string) ([]string, error) {
	dir, err := ResolveDir()
	if err != nil {
		return nil, err
	}

	suffix := "." + direction + ".sql"
	files, err := filepath.Glob(filepath.Join(dir, "*"+suffix))
	if err != nil {
		return nil, err
	}

	sortMigrationFiles(files, direction == "down")
	return files, nil
}

// sortMigrationFiles orders versions numerically across the 9999 boundary.
// Same-prefix stems keep lexical order; the migration ledger still uses the
// entire stem, so neither aliases nor applied-version identity changes.
func sortMigrationFiles(files []string, reverse bool) {
	less := func(a, b string) bool {
		ap, _, _ := strings.Cut(filepath.Base(a), "_")
		bp, _, _ := strings.Cut(filepath.Base(b), "_")
		an, ae := strconv.ParseUint(ap, 10, 64)
		bn, be := strconv.ParseUint(bp, 10, 64)
		if ae == nil && be == nil && an != bn {
			return an < bn
		}
		return a < b
	}
	sort.Slice(files, func(i, j int) bool {
		if reverse {
			return less(files[j], files[i])
		}
		return less(files[i], files[j])
	})
}

// AllVersions returns every "up" migration version found on disk, in apply
// order. The readiness check verifies that all of them are recorded in
// schema_migrations — checking only the lexically-last version would miss an
// out-of-order migration (one numbered below an already-applied later one),
// letting a server report ready while running against a schema that lacks it.
func AllVersions() ([]string, error) {
	files, err := Files("up")
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no up migrations found")
	}
	versions := make([]string, len(files))
	for i, f := range files {
		versions[i] = ExtractVersion(f)
	}
	return versions, nil
}

// ExtractVersion strips the .up.sql / .down.sql suffix from a migration file.
func ExtractVersion(filename string) string {
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, ".up.sql")
	base = strings.TrimSuffix(base, ".down.sql")
	return base
}
