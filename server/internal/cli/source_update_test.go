package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceSpec(t *testing.T) {
	tests := []struct {
		name, repo, ref string
	}{
		{"fork", "https://github.com/D1-2004/multica.git", "develop"},
		{"official", "https://github.com/multica-ai/multica.git", "main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := ResolveSource(tt.name)
			if err != nil {
				t.Fatalf("ResolveSource: %v", err)
			}
			if spec.Name != tt.name || spec.RepoURL != tt.repo || spec.DefaultRef != tt.ref {
				t.Fatalf("spec = %+v", spec)
			}
		})
	}
	if _, err := ResolveSource("unknown"); err == nil {
		t.Fatal("unknown source must fail")
	}
}

func TestUpdateSourcePersistence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, err := LoadUpdateSource(); err != nil || got != "" {
		t.Fatalf("missing source = %q, %v", got, err)
	}
	if err := SaveUpdateSource("fork"); err != nil {
		t.Fatalf("SaveUpdateSource: %v", err)
	}
	if got, err := LoadUpdateSource(); err != nil || got != "fork" {
		t.Fatalf("loaded source = %q, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(home, ".multica", "update-source"))
	if err != nil {
		t.Fatalf("stat source file: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("source file mode = %o, want private", info.Mode().Perm())
	}
}

func TestBuildAndInstallSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("FAKE_COMMAND_LOG", logPath)

	writeExecutable(t, filepath.Join(binDir, "git"), `#!/bin/sh
printf 'git %s\n' "$*" >> "$FAKE_COMMAND_LOG"
if [ "$1" = "clone" ]; then
  mkdir -p "$5/.git" "$5/server"
fi
`)
	writeExecutable(t, filepath.Join(binDir, "go"), `#!/bin/sh
printf 'go cwd=%s args=%s\n' "$PWD" "$*" >> "$FAKE_COMMAND_LOG"
if [ "$FAKE_GO_FAIL" = "1" ]; then
  exit 23
fi
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    printf 'new source binary' > "$1"
    chmod 0755 "$1"
    exit 0
  fi
  shift
done
exit 24
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	destination := filepath.Join(t.TempDir(), "multica")
	if err := os.WriteFile(destination, []byte("old binary"), 0o750); err != nil {
		t.Fatalf("seed destination: %v", err)
	}
	spec, _ := ResolveSource("fork")
	if _, err := BuildAndInstallSource(context.Background(), spec, "", destination); err != nil {
		t.Fatalf("first source install: %v", err)
	}
	if got := readFile(t, destination); got != "new source binary" {
		t.Fatalf("destination = %q", got)
	}
	if info, _ := os.Stat(destination); info.Mode().Perm() != 0o750 {
		t.Fatalf("destination mode = %o, want 750", info.Mode().Perm())
	}
	log := readFile(t, logPath)
	for _, want := range []string{
		"git clone --filter=blob:none --no-checkout " + spec.RepoURL,
		"git -C " + filepath.Join(home, ".multica", "source", "fork") + " fetch --force --depth=1 origin develop",
		"checkout --detach FETCH_HEAD",
		"go cwd=" + filepath.Join(home, ".multica", "source", "fork", "server"),
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("command log missing %q:\n%s", want, log)
		}
	}

	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildAndInstallSource(context.Background(), spec, "feature/chat", destination); err != nil {
		t.Fatalf("existing checkout update: %v", err)
	}
	log = readFile(t, logPath)
	if strings.Contains(log, "git clone") {
		t.Fatalf("existing checkout cloned again:\n%s", log)
	}
	if !strings.Contains(log, "remote set-url origin "+spec.RepoURL) || !strings.Contains(log, "origin feature/chat") {
		t.Fatalf("existing checkout did not update origin/ref:\n%s", log)
	}

	if err := os.WriteFile(destination, []byte("keep me"), 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_GO_FAIL", "1")
	if _, err := BuildAndInstallSource(context.Background(), spec, "broken", destination); err == nil {
		t.Fatal("build failure must be returned")
	}
	if got := readFile(t, destination); got != "keep me" {
		t.Fatalf("failed build replaced destination with %q", got)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
