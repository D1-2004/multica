package daemon

import "strings"

func dispatchRuntimePromptForEnv(task Task) string {
	return strings.TrimSpace(task.DispatchRuntimePrompt)
}
