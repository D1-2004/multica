package modelregistry

import "testing"

func TestTaskRuntimeModelPreservesLegacyCatalogAndProviderBinding(t *testing.T) {
	for _, ref := range []Ref{{"mass", "qwen3.7-plus"}, {"mass", "org/model"}, {"bailian", "qwen3.7-plus"}, {"bailian", "org/model"}} {
		wire := taskRuntimeModel(ref)
		if ref.Provider == "mass" && wire != ref.Model {
			t.Fatal("builtin model no longer matches legacy DSH catalog")
		}
		if ref.Provider != "mass" && wire != ref.String() {
			t.Fatal("lost custom provider qualification")
		}
		s := Snapshot{Config: Config{AgentModels: []Ref{ref}, DefaultModel: ref}}
		got, err := s.AgentRef(wire)
		if err != nil || got != ref {
			t.Fatalf("task model changed provider: got=%v err=%v", got, err)
		}
	}
}
