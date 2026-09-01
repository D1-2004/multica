package assoc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound     = errors.New("assoc: not found")
	ErrInvalidQuery = errors.New("assoc: invalid query")
	ErrInvalidTask  = errors.New("assoc: invalid task")
)

type Store interface {
	InsertTask(ctx context.Context, task Task) (Task, error)
	GetTask(ctx context.Context, id string) (Task, error)
	GetOpenTaskByIssue(ctx context.Context, workspaceID, agentID, issueID string) (Task, error)
	TouchTask(ctx context.Context, id string, at time.Time) error
	ListTasksByIssue(ctx context.Context, workspaceID, agentID, issueID string, since, until time.Time) ([]Task, error)
	ListTasksByIDs(ctx context.Context, ids []string) ([]Task, error)
	ListTasksInWindow(ctx context.Context, workspaceID, agentID string, since, until time.Time) ([]Task, error)
	InsertEdge(ctx context.Context, edge Edge) (Edge, error)
	ListEdgesByDst(ctx context.Context, workspaceID, agentID, dstType, dstID string, since time.Time) ([]Edge, error)
	ListEdgesBySrc(ctx context.Context, workspaceID, agentID, srcType, srcID string) ([]Edge, error)
	CloseSceneAssociations(ctx context.Context, workspaceID, agentID, sceneKey string) (CloseSceneResult, error)
	InsertEvent(ctx context.Context, event Event) (Event, error)
	GetEventByEvidence(ctx context.Context, workspaceID, agentID, evidenceID string) (Event, error)
	ListEventsByScene(ctx context.Context, workspaceID, agentID, sceneKey string, since time.Time, limit int) ([]Event, error)
	UpdateEventTask(ctx context.Context, workspaceID, agentID, evidenceID, taskID string) error
	EnsureScene(ctx context.Context, workspaceID, agentID, sceneKey, kind string, at time.Time) error
	EnsurePerson(ctx context.Context, workspaceID, agentID, personKey, displayName string, aliases []string) error
	ResolvePersonKey(ctx context.Context, workspaceID, agentID, identifier string) (string, error)
}

// Memory is an in-process Store for tests. It is not the production source of truth.
type Memory struct {
	mu      sync.Mutex
	tasks   map[string]Task
	edges   map[string]Edge
	events  map[string]Event
	scenes  map[string]string
	persons map[string]string
	aliases map[string]string
}

func NewMemory() *Memory {
	return &Memory{
		tasks:   map[string]Task{},
		edges:   map[string]Edge{},
		events:  map[string]Event{},
		scenes:  map[string]string{},
		persons: map[string]string{},
		aliases: map[string]string{},
	}
}

func (m *Memory) InsertTask(_ context.Context, task Task) (Task, error) {
	if err := ValidatePurpose(task.Purpose); err != nil {
		return Task{}, err
	}
	if task.WorkspaceID == "" || task.AgentID == "" || task.IssueID == "" {
		return Task{}, ErrInvalidTask
	}
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	if task.Status == "" {
		task.Status = StatusOpen
	}
	now := time.Now().UTC()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = now
	}
	if task.LastTouchedAt.IsZero() {
		task.LastTouchedAt = now
	}
	task.Props = cloneProps(task.Props)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks[task.ID] = task
	return task, nil
}

func (m *Memory) GetTask(_ context.Context, id string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	return task, nil
}

func (m *Memory) GetOpenTaskByIssue(_ context.Context, workspaceID, agentID, issueID string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found Task
	ok := false
	for _, task := range m.tasks {
		if task.WorkspaceID != workspaceID || task.AgentID != agentID || task.IssueID != issueID {
			continue
		}
		if task.Status != StatusOpen && task.Status != StatusWaiting {
			continue
		}
		if !ok || task.LastTouchedAt.After(found.LastTouchedAt) {
			found = task
			ok = true
		}
	}
	if !ok {
		return Task{}, ErrNotFound
	}
	return found, nil
}

func (m *Memory) TouchTask(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[id]
	if !ok {
		return ErrNotFound
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	task.LastTouchedAt = at
	task.UpdatedAt = at
	m.tasks[id] = task
	return nil
}

func (m *Memory) ListTasksByIssue(_ context.Context, workspaceID, agentID, issueID string, since, until time.Time) ([]Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Task, 0)
	for _, task := range m.tasks {
		if task.WorkspaceID != workspaceID || task.AgentID != agentID || task.IssueID != issueID {
			continue
		}
		if !since.IsZero() && task.LastTouchedAt.Before(since) {
			continue
		}
		if !until.IsZero() && task.LastTouchedAt.After(until) {
			continue
		}
		out = append(out, task)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastTouchedAt.After(out[j].LastTouchedAt)
	})
	return out, nil
}

func (m *Memory) ListTasksByIDs(_ context.Context, ids []string) ([]Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Task, 0, len(ids))
	for _, id := range ids {
		if task, ok := m.tasks[id]; ok {
			out = append(out, task)
		}
	}
	return out, nil
}

func (m *Memory) ListTasksInWindow(_ context.Context, workspaceID, agentID string, since, until time.Time) ([]Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Task, 0)
	for _, task := range m.tasks {
		if task.WorkspaceID != workspaceID || task.AgentID != agentID {
			continue
		}
		if !since.IsZero() && task.LastTouchedAt.Before(since) {
			continue
		}
		if !until.IsZero() && task.LastTouchedAt.After(until) {
			continue
		}
		out = append(out, task)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastTouchedAt.After(out[j].LastTouchedAt)
	})
	return out, nil
}

func (m *Memory) InsertEdge(_ context.Context, edge Edge) (Edge, error) {
	if edge.WorkspaceID == "" || edge.AgentID == "" || edge.Rel == "" {
		return Edge{}, ErrInvalidQuery
	}
	if edge.ID == "" {
		edge.ID = uuid.NewString()
	}
	if edge.Status == "" {
		edge.Status = StatusOpen
	}
	now := time.Now().UTC()
	if edge.OpenedAt.IsZero() {
		edge.OpenedAt = now
	}
	if edge.LastTouchedAt.IsZero() {
		edge.LastTouchedAt = now
	}
	edge.Props = cloneProps(edge.Props)
	m.mu.Lock()
	defer m.mu.Unlock()
	if edge.Status == StatusOpen {
		for id, existing := range m.edges {
			if existing.AgentID == edge.AgentID &&
				existing.SrcType == edge.SrcType && existing.SrcID == edge.SrcID &&
				existing.DstType == edge.DstType && existing.DstID == edge.DstID &&
				existing.Rel == edge.Rel && existing.Status == StatusOpen {
				existing.LastTouchedAt = edge.LastTouchedAt
				existing.Props = mergeProps(existing.Props, edge.Props)
				m.edges[id] = existing
				return existing, nil
			}
		}
	}
	m.edges[edge.ID] = edge
	return edge, nil
}

func (m *Memory) ListEdgesByDst(_ context.Context, workspaceID, agentID, dstType, dstID string, since time.Time) ([]Edge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Edge, 0)
	for _, edge := range m.edges {
		if edge.WorkspaceID != workspaceID || edge.AgentID != agentID {
			continue
		}
		if edge.DstType != dstType || edge.DstID != dstID {
			continue
		}
		if !since.IsZero() && edge.LastTouchedAt.Before(since) {
			continue
		}
		out = append(out, edge)
	}
	return out, nil
}

func (m *Memory) ListEdgesBySrc(_ context.Context, workspaceID, agentID, srcType, srcID string) ([]Edge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Edge, 0)
	for _, edge := range m.edges {
		if edge.WorkspaceID != workspaceID || edge.AgentID != agentID {
			continue
		}
		if edge.SrcType != srcType || edge.SrcID != srcID {
			continue
		}
		out = append(out, edge)
	}
	return out, nil
}

func (m *Memory) CloseSceneAssociations(_ context.Context, workspaceID, agentID, sceneKey string) (CloseSceneResult, error) {
	sceneKey = strings.TrimSpace(sceneKey)
	if sceneKey == "" {
		return CloseSceneResult{}, fmt.Errorf("%w: conversation_id is required", ErrInvalidQuery)
	}
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	eventIDs := map[string]struct{}{}
	var result CloseSceneResult
	for id, event := range m.events {
		if event.WorkspaceID != workspaceID || event.AgentID != agentID || event.SceneKey != sceneKey {
			continue
		}
		eventIDs[event.ID] = struct{}{}
		if event.TaskID == "" {
			continue
		}
		event.TaskID = ""
		m.events[id] = event
		result.UnlinkedEvents++
	}
	for id, edge := range m.edges {
		if edge.WorkspaceID != workspaceID || edge.AgentID != agentID || edge.Status == StatusClosed {
			continue
		}
		closeIt := (edge.DstType == NodeScene && edge.DstID == sceneKey) ||
			(edge.SrcType == NodeScene && edge.SrcID == sceneKey)
		if !closeIt && edge.SrcType == NodeEvent {
			_, closeIt = eventIDs[edge.SrcID]
		}
		if !closeIt {
			continue
		}
		closedAt := now
		edge.Status = StatusClosed
		edge.ClosedAt = &closedAt
		edge.LastTouchedAt = now
		m.edges[id] = edge
		result.ClosedEdges++
	}
	return result, nil
}

func (m *Memory) InsertEvent(_ context.Context, event Event) (Event, error) {
	if event.WorkspaceID == "" || event.AgentID == "" || strings.TrimSpace(event.EvidenceID) == "" {
		return Event{}, ErrInvalidQuery
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = event.CreatedAt
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.events {
		if existing.WorkspaceID == event.WorkspaceID &&
			existing.AgentID == event.AgentID &&
			existing.EvidenceID == event.EvidenceID {
			if existing.Direction == "" && event.Direction != "" {
				existing.Direction = event.Direction
			}
			if existing.TaskID == "" && event.TaskID != "" {
				existing.TaskID = event.TaskID
			}
			if existing.Body == "" && event.Body != "" {
				existing.Body = ClipBody(event.Body, EventBodyMaxRunes)
			}
			m.events[existing.ID] = existing
			return existing, nil
		}
	}
	event.Body = ClipBody(event.Body, EventBodyMaxRunes)
	m.events[event.ID] = event
	return event, nil
}

func (m *Memory) UpdateEventTask(_ context.Context, workspaceID, agentID, evidenceID, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, existing := range m.events {
		if existing.WorkspaceID == workspaceID && existing.AgentID == agentID && existing.EvidenceID == evidenceID {
			existing.TaskID = taskID
			m.events[id] = existing
			return nil
		}
	}
	return ErrNotFound
}

func (m *Memory) GetEventByEvidence(_ context.Context, workspaceID, agentID, evidenceID string) (Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.events {
		if existing.WorkspaceID == workspaceID && existing.AgentID == agentID && existing.EvidenceID == evidenceID {
			return existing, nil
		}
	}
	return Event{}, ErrNotFound
}

func (m *Memory) ListEventsByScene(_ context.Context, workspaceID, agentID, sceneKey string, since time.Time, limit int) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, 0)
	for _, existing := range m.events {
		if existing.WorkspaceID != workspaceID || existing.AgentID != agentID {
			continue
		}
		if existing.SceneKey != sceneKey {
			continue
		}
		if !since.IsZero() && existing.OccurredAt.Before(since) {
			continue
		}
		out = append(out, existing)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].OccurredAt.After(out[j].OccurredAt)
	})
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func catalogKey(workspaceID, agentID, id string) string {
	return workspaceID + "\x1f" + agentID + "\x1f" + id
}

func (m *Memory) EnsureScene(_ context.Context, workspaceID, agentID, sceneKey, kind string, at time.Time) error {
	sceneKey = strings.TrimSpace(sceneKey)
	if sceneKey == "" {
		return nil
	}
	if kind == "" {
		kind = "dm"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scenes[catalogKey(workspaceID, agentID, sceneKey)] = kind
	return nil
}

func (m *Memory) EnsurePerson(_ context.Context, workspaceID, agentID, personKey, displayName string, aliases []string) error {
	personKey = strings.TrimSpace(personKey)
	if personKey == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.persons[catalogKey(workspaceID, agentID, personKey)] = displayName
	m.aliases[catalogKey(workspaceID, agentID, personKey)] = personKey
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		m.aliases[catalogKey(workspaceID, agentID, alias)] = personKey
	}
	return nil
}

func (m *Memory) ResolvePersonKey(_ context.Context, workspaceID, agentID, identifier string) (string, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return "", ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if key, ok := m.aliases[catalogKey(workspaceID, agentID, identifier)]; ok {
		return key, nil
	}
	if _, ok := m.persons[catalogKey(workspaceID, agentID, identifier)]; ok {
		return identifier, nil
	}
	return identifier, nil
}
