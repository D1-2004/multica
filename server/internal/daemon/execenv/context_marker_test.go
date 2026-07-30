package execenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureWorkspacesRootMarker covers the root-level daemon marker that
// protects the whole workspaces tree. Regression for the confirmed escape
// where a sandboxed subprocess lost every MULTICA_* env var and ran
// `multica` from the workdir's *parent* directory: the per-workdir marker
// sits below cwd, so the CLI's upward walk found no daemon signal and fell
// back to the user's config PAT, misattributing agent writes to a member.
func TestEnsureWorkspacesRootMarker(t *testing.T) {
	t.Run("writes a valid marker into an empty root", func(t *testing.T) {
		root := t.TempDir()
		if err := EnsureWorkspacesRootMarker(root); err != nil {
			t.Fatalf("EnsureWorkspacesRootMarker: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(root, TaskContextMarkerRelPath))
		if err != nil {
			t.Fatalf("read root marker: %v", err)
		}
		var marker struct {
			ManagedBy string `json:"managed_by"`
		}
		if err := json.Unmarshal(data, &marker); err != nil {
			t.Fatalf("unmarshal root marker: %v\n%s", err, string(data))
		}
		if marker.ManagedBy != TaskContextMarkerManagedBy {
			t.Fatalf("managed_by = %q, want %q", marker.ManagedBy, TaskContextMarkerManagedBy)
		}
	})

	t.Run("is idempotent when a matching marker exists", func(t *testing.T) {
		root := t.TempDir()
		if err := EnsureWorkspacesRootMarker(root); err != nil {
			t.Fatalf("first EnsureWorkspacesRootMarker: %v", err)
		}
		if err := EnsureWorkspacesRootMarker(root); err != nil {
			t.Fatalf("second EnsureWorkspacesRootMarker: %v", err)
		}
	})

	t.Run("refuses to overwrite a foreign file", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, TaskContextMarkerRelPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		foreign := []byte(`{"managed_by":"someone-else"}`)
		if err := os.WriteFile(path, foreign, 0o644); err != nil {
			t.Fatalf("write foreign file: %v", err)
		}
		if err := EnsureWorkspacesRootMarker(root); err == nil {
			t.Fatal("expected error for foreign file at marker path, got nil")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("re-read foreign file: %v", err)
		}
		if string(data) != string(foreign) {
			t.Fatalf("foreign file was clobbered: %s", string(data))
		}
	})

	t.Run("self-heals a corrupt marker from a torn write", func(t *testing.T) {
		// A daemon killed mid os.WriteFile leaves an empty or truncated marker.
		// The old code treated any unparseable file like a foreign one and
		// refused it forever, leaving the whole node without a root guard. The
		// unparseable content is our own torn write, so it must be reclaimed.
		for _, corrupt := range []string{"", "{", "not-json", `{"managed_by":`} {
			root := t.TempDir()
			path := filepath.Join(root, TaskContextMarkerRelPath)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(path, []byte(corrupt), 0o644); err != nil {
				t.Fatalf("seed corrupt marker %q: %v", corrupt, err)
			}
			if err := EnsureWorkspacesRootMarker(root); err != nil {
				t.Fatalf("reclaim corrupt marker %q: unexpected error %v", corrupt, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read reclaimed marker: %v", err)
			}
			var marker struct {
				ManagedBy string `json:"managed_by"`
			}
			if err := json.Unmarshal(data, &marker); err != nil {
				t.Fatalf("reclaimed marker not valid JSON for input %q: %v\n%s", corrupt, err, string(data))
			}
			if marker.ManagedBy != TaskContextMarkerManagedBy {
				t.Fatalf("managed_by = %q, want %q (input %q)", marker.ManagedBy, TaskContextMarkerManagedBy, corrupt)
			}
		}
	})

	t.Run("rejects empty root", func(t *testing.T) {
		if err := EnsureWorkspacesRootMarker(""); err == nil {
			t.Fatal("expected error for empty workspaces root, got nil")
		}
	})
}

// TestPrepare_WritesWorkspacesRootMarker verifies Prepare self-heals the
// root-level marker on every task start, so a marker deleted while the
// daemon is running is restored before the next agent spawns.
func TestPrepare_WritesWorkspacesRootMarker(t *testing.T) {
	root := t.TempDir()
	env, err := Prepare(PrepareParams{
		WorkspacesRoot: root,
		WorkspaceID:    "ws-test-001",
		TaskID:         "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		AgentName:      "Test Agent",
		Task: TaskContextForEnv{
			IssueID: "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		},
	}, testLogger())
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer env.Cleanup(true)

	data, err := os.ReadFile(filepath.Join(root, TaskContextMarkerRelPath))
	if err != nil {
		t.Fatalf("read root marker after Prepare: %v", err)
	}
	if !strings.Contains(string(data), TaskContextMarkerManagedBy) {
		t.Fatalf("root marker missing managed_by discriminator: %s", string(data))
	}
}

// TestReuse_SelfHealsWorkspacesRootMarker verifies the root marker is restored
// on the reuse path too: a marker deleted while the daemon runs must be back
// before a reused task spawns, not only after the next fresh Prepare.
func TestReuse_SelfHealsWorkspacesRootMarker(t *testing.T) {
	root := t.TempDir()
	env, err := Prepare(PrepareParams{
		WorkspacesRoot: root,
		WorkspaceID:    "ws-reuse-001",
		TaskID:         "b2c3d4e5-f6a7-8901-bcde-f23456789012",
		AgentName:      "Reuse Agent",
		Task:           TaskContextForEnv{IssueID: "b2c3d4e5-f6a7-8901-bcde-f23456789012"},
	}, testLogger())
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer env.Cleanup(true)

	rootMarker := filepath.Join(root, TaskContextMarkerRelPath)
	if err := os.Remove(rootMarker); err != nil {
		t.Fatalf("remove root marker: %v", err)
	}

	reused := Reuse(ReuseParams{
		WorkspacesRoot: root,
		WorkDir:        env.WorkDir,
		Task:           TaskContextForEnv{IssueID: "b2c3d4e5-f6a7-8901-bcde-f23456789012"},
	}, testLogger())
	if reused == nil {
		t.Fatal("Reuse returned nil for an existing workdir")
	}
	if _, err := os.Stat(rootMarker); err != nil {
		t.Fatalf("Reuse did not restore the root marker: %v", err)
	}
}

func TestReuse_SessionContextCompatibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		first      TaskContextForEnv
		next       TaskContextForEnv
		compatible bool
	}{
		{
			name: "same durable context remains compatible",
			first: TaskContextForEnv{
				AgentID:           "agent-1",
				AgentName:         "须莫v6",
				AgentInstructions: "Use first person.",
				AgentSkills: []SkillContextForEnv{{
					Name: "memory",
					Files: []SkillFileContextForEnv{
						{Path: "references/b.md", Content: "b"},
						{Path: "references/a.md", Content: "a"},
					},
				}},
				WorkspaceContext: "workspace rules",
			},
			next: TaskContextForEnv{
				IssueID:           "a different per-turn issue",
				TriggerCommentID:  "a different per-turn comment",
				AgentID:           "agent-1",
				AgentName:         "须莫v6",
				AgentInstructions: "Use first person.",
				AgentSkills: []SkillContextForEnv{{
					Name: "memory",
					Files: []SkillFileContextForEnv{
						{Path: "references/a.md", Content: "a"},
						{Path: "references/b.md", Content: "b"},
					},
				}},
				WorkspaceContext: "workspace rules",
			},
			compatible: true,
		},
		{
			name: "agent rename invalidates provider transcript",
			first: TaskContextForEnv{
				AgentID:   "agent-1",
				AgentName: "须莫v4 Pi",
			},
			next: TaskContextForEnv{
				AgentID:   "agent-1",
				AgentName: "须莫v6",
			},
			compatible: false,
		},
		{
			name: "skill content change invalidates provider transcript",
			first: TaskContextForEnv{
				AgentID: "agent-1",
				AgentSkills: []SkillContextForEnv{{
					Name:    "identity",
					Content: "old identity",
				}},
			},
			next: TaskContextForEnv{
				AgentID: "agent-1",
				AgentSkills: []SkillContextForEnv{{
					Name:    "identity",
					Content: "new identity",
				}},
			},
			compatible: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			env, err := Prepare(PrepareParams{
				WorkspacesRoot: root,
				WorkspaceID:    "ws-context-compat",
				TaskID:         "11111111-2222-3333-4444-555555555555",
				Provider:       "pi",
				Task:           tt.first,
			}, testLogger())
			if err != nil {
				t.Fatalf("Prepare failed: %v", err)
			}
			defer env.Cleanup(true)

			reused := Reuse(ReuseParams{
				WorkspacesRoot: root,
				WorkDir:        env.WorkDir,
				Provider:       "pi",
				Task:           tt.next,
			}, testLogger())
			if reused == nil {
				t.Fatal("Reuse returned nil")
			}
			if reused.ResumeContextCompatible != tt.compatible {
				t.Fatalf("ResumeContextCompatible = %v, want %v", reused.ResumeContextCompatible, tt.compatible)
			}
		})
	}
}

func TestReuse_LegacyMarkerStartsFreshSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	task := TaskContextForEnv{
		AgentID:   "agent-legacy",
		AgentName: "Current Agent",
	}
	env, err := Prepare(PrepareParams{
		WorkspacesRoot: root,
		WorkspaceID:    "ws-legacy-context",
		TaskID:         "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Provider:       "pi",
		Task:           task,
	}, testLogger())
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer env.Cleanup(true)

	markerPath := filepath.Join(env.WorkDir, TaskContextMarkerRelPath)
	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	var marker map[string]any
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatalf("unmarshal marker: %v", err)
	}
	delete(marker, "session_context_sha256")
	data, err = json.Marshal(marker)
	if err != nil {
		t.Fatalf("marshal legacy marker: %v", err)
	}
	if err := os.WriteFile(markerPath, data, 0o644); err != nil {
		t.Fatalf("write legacy marker: %v", err)
	}

	reused := Reuse(ReuseParams{
		WorkspacesRoot: root,
		WorkDir:        env.WorkDir,
		Provider:       "pi",
		Task:           task,
	}, testLogger())
	if reused == nil {
		t.Fatal("Reuse returned nil")
	}
	if reused.ResumeContextCompatible {
		t.Fatal("legacy marker without a context digest must start a fresh provider session")
	}
}
