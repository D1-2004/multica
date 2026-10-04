package migrations

import (
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestMigrationOrderCrossesFiveDigitBoundary(t *testing.T) {
	files := []string{"10062_source_guard.up.sql", "10060_once.up.sql", "9520_context_scope_routine.up.sql", "9976_existing.up.sql", "10061_source.up.sql", "042_autopilot.up.sql", "020_z.up.sql", "020_a.up.sql"}
	want := []string{"020_a.up.sql", "020_z.up.sql", "042_autopilot.up.sql", "9520_context_scope_routine.up.sql", "9976_existing.up.sql", "10060_once.up.sql", "10061_source.up.sql", "10062_source_guard.up.sql"}
	sortMigrationFiles(files, false)
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("schema dependency order: %v", files)
	}
	sortMigrationFiles(files, true)
	for i := range files {
		if files[i] != want[len(want)-1-i] {
			t.Fatalf("rollback did not reverse apply order: %v", files)
		}
	}
}

func TestMigrationOrderPreservesExistingFourDigitSequence(t *testing.T) {
	files, err := Files("up")
	if err != nil {
		t.Fatal(err)
	}
	var old []string
	for _, file := range files {
		prefix, _, _ := strings.Cut(filepath.Base(file), "_")
		n, err := strconv.Atoi(prefix)
		if err == nil && n < 10000 {
			old = append(old, file)
		}
	}
	lexical := append([]string(nil), old...)
	sort.Strings(lexical)
	if !reflect.DeepEqual(old, lexical) {
		t.Fatal("numeric sorting changed the existing pre-10000 migration order")
	}
}
