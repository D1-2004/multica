package employeeentry

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/employeetask"
)

type noopTaskOriginReader struct{}

func (noopTaskOriginReader) ReadTaskOrigin(context.Context, DB, Scope, employeetask.Task, employeetask.Entry) (TaskOrigin, error) {
	return TaskOrigin{}, nil
}

func TestTaskOriginRegistryMustRegisterFailsLoudlyOnDuplicate(t *testing.T) {
	r := NewTaskOriginRegistry()
	r.MustRegister("scene.routine.schedule", noopTaskOriginReader{})
	defer func() {
		if recover() == nil {
			t.Fatal("a second reader for one namespace was accepted silently")
		}
	}()
	r.MustRegister("scene.routine.schedule", noopTaskOriginReader{})
}
