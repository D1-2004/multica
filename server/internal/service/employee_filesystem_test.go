package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

func TestEmployeeFilesystemScopeStableAcrossProvidersAndDistinctAcrossSessions(t *testing.T) {
	a := dshhost.SessionScope{Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, Kind: "chat", ID: uuid.New()}
	first := employeeFilesystemScopeID(a)
	if employeeFilesystemScopeID(a) != first || first == uuid.Nil {
		t.Fatal("scope changed without a session change")
	}
	for _, change := range []func(*dshhost.SessionScope){
		func(s *dshhost.SessionScope) { s.ID = uuid.New() },
		func(s *dshhost.SessionScope) { s.Kind = "issue" },
		func(s *dshhost.SessionScope) { s.AgentID = uuid.New() },
	} {
		other := a
		change(&other)
		if employeeFilesystemScopeID(other) == first {
			t.Fatal("independent sessions reused an execution scope")
		}
	}
}
