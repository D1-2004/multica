package handler

// The single seam between the Employee wake input builders and Employee
// memory. buildInput (chat) opens an employeeMemoryWake when it starts, so a
// step may begin a bounded read in parallel, releases it on return and calls
// freeze once at the end; buildTaskWakeInput calls freeze once; processClaimed
// calls the write-side hook once after a wake settles. Memory features add steps to
// freeze/afterWake here instead of editing those builders. Everything frozen
// here is part of the input snapshot: a replayed job reads the snapshot and
// never calls these functions again, so old snapshots replay byte-identically.

import (
	"context"

	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// employeeMemoryInputMeta is embedded in employeeSavedInput. Its fields are
// omitempty and never reach the model; older binaries ignore them.
type employeeMemoryInputMeta struct {
	// MemoryManifest lists the learning records injected into Input.Memory.
	MemoryManifest []employeememory.ManifestEntry `json:"memory_manifest,omitempty"`
	MemoryStats    *employeememory.BriefStats     `json:"memory_stats,omitempty"`
}

// employeeMemoryWake carries what memory steps may read for one new input.
type employeeMemoryWake struct {
	worker *EmployeeSceneWorker
	job    employeeentry.Job
	// Chat wakes: the window after Host memory commands and the original one.
	work, original []employeeDispatchEnvelope
	// Task wakes: the fenced directory row, the Task origin and its goal.
	task *employeeMemoryTaskWake
	// stops cancels reads a step started early; release runs them.
	stops []func()
	// chatScene is the chat wake's fenced directory row, set by freezeBrief.
	chatScene *db.AgentScene
}

type employeeMemoryTaskWake struct {
	registered db.AgentScene
	origin     employeeentry.TaskOrigin
	goal       string
}

// employeeChatMemory opens the memory steps of a chat wake when buildInput
// starts. ctx bounds any read a step starts here.
func (w *EmployeeSceneWorker) employeeChatMemory(ctx context.Context, job employeeentry.Job, work, original []employeeDispatchEnvelope) *employeeMemoryWake {
	return &employeeMemoryWake{worker: w, job: job, work: work, original: original}
}

func (w *EmployeeSceneWorker) employeeTaskWakeMemory(job employeeentry.Job, registered db.AgentScene, origin employeeentry.TaskOrigin, goal string) *employeeMemoryWake {
	return &employeeMemoryWake{worker: w, job: job, task: &employeeMemoryTaskWake{registered: registered, origin: origin, goal: goal}}
}

// release stops reads that freeze did not join, for example when the builder
// returns early with an error.
func (m *employeeMemoryWake) release() {
	for _, stop := range m.stops {
		stop()
	}
}

// freeze adds every memory-owned part of a new input snapshot.
func (m *employeeMemoryWake) freeze(ctx context.Context, input *employeeSavedInput) error {
	if err := m.freezeBrief(ctx, input); err != nil {
		return err
	}
	m.freezeVerbatimRecall(ctx, input)
	// Persona facts (self profile, language, scene status, group members) are
	// frozen into the same new snapshot.
	wake := employeePersonaChatWake(m.job, m.work)
	if m.task != nil {
		wake = employeePersonaTaskWake(m.task.origin)
	}
	m.worker.applyEmployeePersonaInput(ctx, m.job, wake, input)
	return nil
}

// employeeMemoryAfterWake is the write-side hook: it runs once after a wake's
// outcome is saved and its effects are committed (or the attempt ended). It
// must never change the job's result; durable follow-up work belongs in
// PostgreSQL state written here, not in process memory.
func (w *EmployeeSceneWorker) employeeMemoryAfterWake(ctx context.Context, job employeeentry.Job, saved employeeSavedOutcome, committed bool) {
}

// employeeMemoryBriefLabels reports whether this snapshot's memory tools
// resolve "[mN]" labels; until they do, the brief carries record UUIDs.
func employeeMemoryBriefLabels(input *employeeSavedInput) bool {
	return employeeMemoryToolsV2Frozen(*input)
}

// employeeTaskWakePrivateRequester is shared by the brief and history readers.
// The PostgreSQL origin and fenced directory row, never wake payload labels,
// select one tenant-qualified owner. Automation and public audiences fail closed.
func employeeTaskWakePrivateRequester(registered db.AgentScene, origin employeeentry.TaskOrigin) string {
	r := origin.Anchor.RequesterRef
	if registered.SceneKind != scene.KindDM || !origin.Anchor.Conversation ||
		origin.Anchor.SceneID != util.UUIDToString(registered.ID) || r != origin.Task.RequesterRef ||
		service.IsAutomationRequesterRef(r) || !employeememory.PersonViewRef(registered.TenantOrgID, r) {
		return ""
	}
	return r
}

// freezeBrief replaces Input.Memory with the foreground brief v2.
func (m *employeeMemoryWake) freezeBrief(ctx context.Context, input *employeeSavedInput) error {
	h := m.worker.handler
	if h.EmployeeMemory == nil {
		return nil
	}
	var registered db.AgentScene
	requester, query := "", ""
	if m.task != nil {
		registered = m.task.registered
		// Requester-private context only in a 1:1 scene with the Task's own
		// requester; automation origins never read private memory.
		origin := m.task.origin
		requester = employeeTaskWakePrivateRequester(registered, origin)
		query = employeememory.ForegroundQuery([]string{m.task.goal, origin.RequestText}, nil)
	} else {
		var err error
		if registered, err = employeeSceneFence(ctx, h, m.job); err != nil {
			return err
		}
		m.chatScene = &registered
		original := []employeeSourceMessage{}
		for i, item := range m.job.Items {
			original = append(original, employeeSourceMessages(item, m.original[i])...)
		}
		if r, unique := employeeAutomaticPrivateRequester(registered, original); unique {
			requester = r
		}
		var current, quoted []string
		for i, item := range m.job.Items {
			for _, source := range employeeSourceMessages(item, m.work[i]) {
				current = append(current, source.Message.Text)
				if ref := source.Message.ReferencedMessage; ref != nil {
					quoted = append(quoted, ref.Text)
				}
			}
		}
		query = employeememory.ForegroundQuery(current, quoted)
	}
	sceneScope := employeememory.Scope{WorkspaceID: parseUUID(m.job.Scope.WorkspaceID), AgentID: parseUUID(m.job.Scope.AgentID), TenantOrgID: m.job.Scope.TenantOrgID, Scene: scene.Ref{SceneID: m.job.Scope.SceneID}, Kind: employeememory.ScopeScene}
	brief, err := h.EmployeeMemory.ForegroundBrief(ctx, employeememory.ForegroundRequest{Scene: sceneScope, SceneKind: registered.SceneKind, Query: query, Requester: requester, Labels: employeeMemoryBriefLabels(input)})
	if err != nil {
		input.Input.Memory = "Scene memory unavailable."
		langfuse.TraceFromContext(ctx).AddMetadata(map[string]any{"memory_status": "unavailable"})
		return nil
	}
	input.Input.Memory = brief.Text
	input.MemoryManifest = brief.Manifest
	input.MemoryStats = &brief.Stats
	employeeTraceMemory(ctx, brief)
	return nil
}

// freezeVerbatimRecall appends the [O] section of a chat group wake: older
// human lines of the 14-day group transcript that share topical units with
// the current window. Lines already in front of the model (the window, the
// recent conversation and the frozen group transcript) are excluded. It runs
// after the brief replaced Input.Memory and never fails the wake.
func (m *employeeMemoryWake) freezeVerbatimRecall(ctx context.Context, input *employeeSavedInput) {
	if m.task != nil || m.chatScene == nil {
		return
	}
	messages := []employeeSourceMessage{}
	for i, item := range m.job.Items {
		messages = append(messages, employeeSourceMessages(item, m.work[i])...)
	}
	m.worker.handler.appendEmployeeVerbatimRecall(ctx, input, m.job, *m.chatScene, messages)
}

// employeeTraceMemory records what was injected: ids, sections and sizes,
// never the memory text.
func employeeTraceMemory(ctx context.Context, brief employeememory.ForegroundBrief) {
	hits := brief.Stats.Retrieved + brief.Stats.Verified
	langfuse.TraceFromContext(ctx).AddMetadata(map[string]any{
		"memory_status":           "loaded",
		"memory_manifest":         employeeTraceSafe(brief.Manifest),
		"memory_query_terms":      employeeTraceSafe(brief.Stats.QueryTerms),
		"memory_hits":             hits,
		"memory_pinned":           brief.Stats.Pinned,
		"memory_person_view":      brief.Stats.PersonView,
		"memory_bytes":            len(brief.Text),
		"memory_bytes_by_section": brief.Stats.BytesBySection,
	})
}
