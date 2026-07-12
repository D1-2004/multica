package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const updateSourceFileName = "update-source"

type SourceSpec struct {
	Name       string
	RepoURL    string
	DefaultRef string
}

func ResolveSource(name string) (SourceSpec, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "fork":
		return SourceSpec{
			Name:       "fork",
			RepoURL:    "https://github.com/D1-2004/multica.git",
			DefaultRef: "develop",
		}, nil
	case "official":
		return SourceSpec{
			Name:       "official",
			RepoURL:    "https://github.com/multica-ai/multica.git",
			DefaultRef: "main",
		}, nil
	default:
		return SourceSpec{}, fmt.Errorf("unknown update source %q: use fork or official", name)
	}
}

func updateSourcePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".multica", updateSourceFileName), nil
}

// LoadUpdateSource returns an empty source for historical release/Homebrew
// installs that do not have a persisted source selection.
func LoadUpdateSource() (string, error) {
	path, err := updateSourcePath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read update source: %w", err)
	}
	name := strings.TrimSpace(string(data))
	if name == "" {
		return "", nil
	}
	spec, err := ResolveSource(name)
	if err != nil {
		return "", fmt.Errorf("invalid persisted update source: %w", err)
	}
	return spec.Name, nil
}

func SaveUpdateSource(name string) error {
	spec, err := ResolveSource(name)
	if err != nil {
		return err
	}
	path, err := updateSourcePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create update source directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-source-*")
	if err != nil {
		return fmt.Errorf("create update source temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure update source temp file: %w", err)
	}
	if _, err := tmp.WriteString(spec.Name + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write update source: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close update source: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace update source: %w", err)
	}
	return nil
}

func CurrentExecutablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve executable symlink: %w", err)
	}
	return resolved, nil
}

// BuildAndInstallSource refreshes one managed source checkout, builds only the
// CLI, and atomically replaces destination after a successful build.
func BuildAndInstallSource(ctx context.Context, spec SourceSpec, ref, destination string) (string, error) {
	resolved, err := ResolveSource(spec.Name)
	if err != nil {
		return "", err
	}
	if spec.RepoURL != resolved.RepoURL || spec.DefaultRef != resolved.DefaultRef {
		return "", fmt.Errorf("source spec for %q does not match the source registry", spec.Name)
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = spec.DefaultRef
	}
	if strings.TrimSpace(destination) == "" {
		return "", fmt.Errorf("destination binary path is required")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	sourceRoot := filepath.Join(home, ".multica", "source")
	sourceDir := filepath.Join(sourceRoot, spec.Name)
	if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
		return "", fmt.Errorf("create source root: %w", err)
	}

	gitDir := filepath.Join(sourceDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		if _, sourceErr := os.Stat(sourceDir); sourceErr == nil {
			return "", fmt.Errorf("managed source path %s exists but is not a git checkout", sourceDir)
		} else if !os.IsNotExist(sourceErr) {
			return "", fmt.Errorf("inspect managed source path: %w", sourceErr)
		}
		if _, err := runSourceCommand(ctx, "", "git", "clone", "--filter=blob:none", "--no-checkout", spec.RepoURL, sourceDir); err != nil {
			_ = os.RemoveAll(sourceDir)
			return "", fmt.Errorf("clone %s source: %w", spec.Name, err)
		}
	} else if err != nil {
		return "", fmt.Errorf("inspect managed git checkout: %w", err)
	} else {
		if _, err := runSourceCommand(ctx, "", "git", "-C", sourceDir, "remote", "set-url", "origin", spec.RepoURL); err != nil {
			return "", fmt.Errorf("refresh %s origin: %w", spec.Name, err)
		}
	}
	if _, err := runSourceCommand(ctx, "", "git", "-C", sourceDir, "fetch", "--force", "--depth=1", "origin", ref); err != nil {
		return "", fmt.Errorf("fetch %s ref %q: %w", spec.Name, ref, err)
	}
	if _, err := runSourceCommand(ctx, "", "git", "-C", sourceDir, "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return "", fmt.Errorf("checkout %s ref %q: %w", spec.Name, ref, err)
	}

	destinationDir := filepath.Dir(destination)
	if err := os.MkdirAll(destinationDir, 0o755); err != nil {
		return "", fmt.Errorf("create binary directory: %w", err)
	}
	mode := os.FileMode(0o755)
	if info, err := os.Stat(destination); err == nil {
		mode = info.Mode()
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat destination binary: %w", err)
	}
	tmp, err := os.CreateTemp(destinationDir, ".multica-source-build-*")
	if err != nil {
		return "", fmt.Errorf("create source build target: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close source build target: %w", err)
	}
	_ = os.Remove(tmpPath)
	defer os.Remove(tmpPath)

	buildDate := time.Now().UTC().Format(time.RFC3339)
	ldflags := strings.Join([]string{
		"-X", "main.version=source-" + spec.Name,
		"-X", "main.commit=" + ref,
		"-X", "main.date=" + buildDate,
	}, " ")
	serverDir := filepath.Join(sourceDir, "server")
	if _, err := runSourceCommand(ctx, serverDir, "go", "build", "-trimpath", "-ldflags", ldflags, "-o", tmpPath, "./cmd/multica"); err != nil {
		return "", fmt.Errorf("build %s CLI from ref %q: %w", spec.Name, ref, err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return "", fmt.Errorf("set source binary permissions: %w", err)
	}
	if err := replaceBinary(tmpPath, destination); err != nil {
		return "", fmt.Errorf("replace binary: %w", err)
	}
	return fmt.Sprintf("Built %s@%s and replaced %s", spec.Name, ref, destination), nil
}

func runSourceCommand(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message != "" {
			return message, fmt.Errorf("%s: %w", message, err)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
