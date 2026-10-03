package handler

import (
	"context"

	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

// EmployeeMemoryReplicaMarker gates memory tools v2. New chat inputs freeze
// the v2 schemas only while every live replica advertises this marker, so an
// older replica never claims a job whose tool calls it cannot execute.
// Already frozen jobs keep the schema they were built with.
const EmployeeMemoryReplicaMarker = "[employee-memory:1]"

// employeeSceneToolsForMemory returns the scene tools with the memory tools
// of the requested version in their usual position.
func employeeSceneToolsForMemory(v2 bool) []employeeloop.Tool {
	tools := employeeSceneTools()
	if !v2 {
		return tools
	}
	replacement := map[string]employeeloop.Tool{}
	for _, tool := range employeeMemoryToolsV2() {
		replacement[tool.Name] = tool
	}
	for i, tool := range tools {
		if next, ok := replacement[tool.Name]; ok {
			tools[i] = next
		}
	}
	return tools
}

// newInputTools chooses the tool schemas frozen into a new chat input: v2
// memory tools only while every live replica supports them. An unavailable or
// failing readiness check keeps v1, which every replica can execute.
func (w *EmployeeSceneWorker) newInputTools(ctx context.Context) []employeeloop.Tool {
	ready := false
	if w.MemoryToolsReady != nil {
		ok, err := w.MemoryToolsReady(ctx)
		ready = err == nil && ok
	}
	return employeeSceneToolsForMemory(ready)
}
