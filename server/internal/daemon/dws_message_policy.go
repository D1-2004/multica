package daemon

import (
	"fmt"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func installTaskDWSShim(agentEnv map[string]string, envRoot string, required bool) error {
	shimDir, err := execenv.EnsureDWSShim(envRoot)
	if err != nil {
		return fmt.Errorf("install task DWS message policy: %w", err)
	}
	if shimDir == "" {
		if required {
			return fmt.Errorf("DWS message policy requires a supported task environment")
		}
		return nil
	}
	basePath := agentEnv["PATH"]
	if basePath == "" {
		basePath = os.Getenv("PATH")
	}
	paths := []string{shimDir}
	for _, dir := range strings.Split(basePath, string(os.PathListSeparator)) {
		if dir != shimDir {
			paths = append(paths, dir)
		}
	}
	agentEnv["PATH"] = strings.Join(paths, string(os.PathListSeparator))
	return nil
}
