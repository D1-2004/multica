package execenv

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestDSHNativePersistentWorkspaceSurvivesTaskCleanup(t *testing.T) {
	for _, removeAll := range []bool{false, true} {
		workdir := t.TempDir()
		sentinel := filepath.Join(workdir, "employee-project.txt")
		if err := os.WriteFile(sentinel, []byte("persistent employee content"), 0600); err != nil {
			t.Fatal(err)
		}
		scratch := filepath.Join(t.TempDir(), "scratch")
		if err := os.Mkdir(scratch, 0700); err != nil {
			t.Fatal(err)
		}
		original := Environment{RootDir: scratch, WorkDir: workdir, PersistentWorkDir: true, logger: slog.Default()}
		// The execution preparer crosses a JSON subprocess boundary.
		raw, _ := json.Marshal(original)
		var restored Environment
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		restored.logger = slog.Default()
		if restored.LocalDirectory || !restored.PersistentWorkDir {
			t.Fatal("persistent cloud ownership was lost or misclassified")
		}
		if err := restored.Cleanup(removeAll); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(sentinel); err != nil || string(got) != "persistent employee content" {
			t.Fatal("task cleanup deleted employee workspace")
		}
		_, err := os.Stat(scratch)
		if removeAll && !os.IsNotExist(err) {
			t.Fatal("task scratch was not cleaned")
		}
	}
}
