package handler

import (
	"github.com/multica-ai/multica/server/internal/scene"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// employeeAutomaticPrivateRequester uses the fenced directory row, not wire
// conversation labels. A single speaker in a group is still a group audience;
// private memory there remains available only through a scoped explicit lookup.
func employeeAutomaticPrivateRequester(registered db.AgentScene, messages []employeeSourceMessage) (string, bool) {
	if registered.SceneKind != scene.KindDM {
		return "", false
	}
	return employeeUniqueRequester(messages)
}
