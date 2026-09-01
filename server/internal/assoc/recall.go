package assoc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

func (q Query) validate() error {
	if strings.TrimSpace(q.WorkspaceID) == "" || strings.TrimSpace(q.AgentID) == "" {
		return fmt.Errorf("%w: workspace_id and agent_id are required", ErrInvalidQuery)
	}
	if q.Since.IsZero() {
		return fmt.Errorf("%w: since is required", ErrInvalidQuery)
	}
	if strings.TrimSpace(q.ConversationID) == "" && strings.TrimSpace(q.IssueID) == "" && strings.TrimSpace(q.Q) == "" {
		return fmt.Errorf("%w: conversation_id, issue, or q is required", ErrInvalidQuery)
	}
	return nil
}

func normalizeLimit(n int) int {
	if n <= 0 {
		return DefaultLimit
	}
	if n > MaxLimit {
		return MaxLimit
	}
	return n
}

func Recall(ctx context.Context, store Store, q Query) (Result, error) {
	if err := q.validate(); err != nil {
		return Result{}, err
	}
	until := q.Until
	if until.IsZero() {
		until = time.Now().UTC()
	}
	limit := normalizeLimit(q.Limit)

	var tasks []Task
	var err error
	if q.IssueID != "" {
		tasks, err = store.ListTasksByIssue(ctx, q.WorkspaceID, q.AgentID, q.IssueID, q.Since, until)
		if err != nil {
			return Result{}, err
		}
	} else if q.ConversationID == "" && strings.TrimSpace(q.Q) != "" {
		tasks, err = store.ListTasksInWindow(ctx, q.WorkspaceID, q.AgentID, q.Since, until)
		if err != nil {
			return Result{}, err
		}
	}
	var sceneEvents []Event
	if q.ConversationID != "" {
		cidTasks, cidErr := tasksForConversation(ctx, store, q)
		if cidErr != nil {
			return Result{}, cidErr
		}
		var evErr error
		sceneEvents, evErr = store.ListEventsByScene(ctx, q.WorkspaceID, q.AgentID, q.ConversationID, q.Since, MaxLimit)
		if evErr != nil {
			return Result{}, evErr
		}
		eventTasks, eventErr := tasksFromEvents(ctx, store, q, sceneEvents)
		if eventErr != nil {
			return Result{}, eventErr
		}
		cidTasks = unionTasks(cidTasks, eventTasks)
		if q.IssueID == "" {
			tasks = cidTasks
		} else {
			tasks = intersectTasks(tasks, cidTasks)
		}
	}
	personHit := map[string]struct{}{}
	if q.PersonID != "" {
		personTasks, personErr := tasksForPerson(ctx, store, q)
		if personErr != nil {
			return Result{}, personErr
		}
		for _, task := range personTasks {
			personHit[task.ID] = struct{}{}
		}
		// A known scene is enough to recall. Person is a rank signal so a
		// uid/staffId/openDingTalkId mismatch cannot hide an outreach cid.
		if q.ConversationID == "" {
			tasks = intersectTasks(tasks, personTasks)
		}
	}

	items := make([]Item, 0, len(tasks))
	needle := strings.ToLower(strings.TrimSpace(q.Q))
	intent := strings.TrimSpace(q.Intent)
	now := until
	for _, task := range tasks {
		if task.LastTouchedAt.Before(q.Since) || task.LastTouchedAt.After(until) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(task.Purpose), needle) {
			continue
		}
		if intent != "" && task.Intent != intent {
			continue
		}
		item, hydErr := hydrateItem(ctx, store, q, task, now)
		if hydErr != nil {
			return Result{}, hydErr
		}
		if _, ok := personHit[task.ID]; ok {
			item.Score *= 1.35
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score == items[j].Score {
			return items[i].LastTouchedAt.After(items[j].LastTouchedAt)
		}
		return items[i].Score > items[j].Score
	})
	if len(items) > limit {
		items = items[:limit]
	}
	if items == nil {
		items = []Item{}
	}
	return Result{
		Since:  q.Since,
		Until:  until,
		Items:  items,
		Events: eventRefs(sceneEvents),
	}, nil
}

func tasksForConversation(ctx context.Context, store Store, q Query) ([]Task, error) {
	edges, err := store.ListEdgesByDst(ctx, q.WorkspaceID, q.AgentID, NodeScene, q.ConversationID, q.Since)
	if err != nil {
		return nil, err
	}
	return tasksFromEdges(ctx, store, q, edges)
}

func tasksForPerson(ctx context.Context, store Store, q Query) ([]Task, error) {
	ids := []string{strings.TrimSpace(q.PersonID)}
	if resolved, err := store.ResolvePersonKey(ctx, q.WorkspaceID, q.AgentID, q.PersonID); err == nil && resolved != "" {
		ids = append(ids, resolved)
	}
	var all []Edge
	seen := map[string]struct{}{}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		edges, err := store.ListEdgesByDst(ctx, q.WorkspaceID, q.AgentID, NodePerson, id, q.Since)
		if err != nil {
			return nil, err
		}
		all = append(all, edges...)
	}
	return tasksFromEdges(ctx, store, q, all)
}

func tasksFromEdges(ctx context.Context, store Store, q Query, edges []Edge) ([]Task, error) {
	ids := make([]string, 0, len(edges))
	seen := map[string]struct{}{}
	for _, edge := range edges {
		if edge.Status == StatusClosed {
			continue
		}
		taskID := taskIDFromEdge(edge)
		if taskID == "" {
			continue
		}
		if _, ok := seen[taskID]; ok {
			continue
		}
		seen[taskID] = struct{}{}
		ids = append(ids, taskID)
	}
	tasks, err := store.ListTasksByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if task.WorkspaceID != q.WorkspaceID || task.AgentID != q.AgentID {
			continue
		}
		out = append(out, task)
	}
	return out, nil
}

func taskIDFromEdge(edge Edge) string {
	if edge.SrcType == NodeTask {
		return edge.SrcID
	}
	if edge.DstType == NodeTask {
		return edge.DstID
	}
	return ""
}

func intersectTasks(a, b []Task) []Task {
	if a == nil {
		return b
	}
	index := map[string]struct{}{}
	for _, task := range b {
		index[task.ID] = struct{}{}
	}
	out := make([]Task, 0)
	for _, task := range a {
		if _, ok := index[task.ID]; ok {
			out = append(out, task)
		}
	}
	return out
}

func unionTasks(a, b []Task) []Task {
	seen := map[string]struct{}{}
	out := make([]Task, 0, len(a)+len(b))
	for _, group := range [][]Task{a, b} {
		for _, task := range group {
			if task.ID == "" {
				continue
			}
			if _, ok := seen[task.ID]; ok {
				continue
			}
			seen[task.ID] = struct{}{}
			out = append(out, task)
		}
	}
	return out
}

func tasksFromEvents(ctx context.Context, store Store, q Query, events []Event) ([]Task, error) {
	ids := make([]string, 0, len(events))
	seen := map[string]struct{}{}
	for _, event := range events {
		taskID := strings.TrimSpace(event.TaskID)
		if taskID == "" {
			continue
		}
		if _, ok := seen[taskID]; ok {
			continue
		}
		seen[taskID] = struct{}{}
		ids = append(ids, taskID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	tasks, err := store.ListTasksByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if task.WorkspaceID != q.WorkspaceID || task.AgentID != q.AgentID {
			continue
		}
		out = append(out, task)
	}
	return out, nil
}

func eventRefs(events []Event) []EventRef {
	out := make([]EventRef, 0, len(events))
	seen := map[string]struct{}{}
	for _, event := range events {
		key := strings.TrimSpace(event.EvidenceID)
		if key == "" {
			key = event.ID
		}
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, EventRef{
			ID:         event.ID,
			Direction:  event.Direction,
			Source:     event.Source,
			EvidenceID: event.EvidenceID,
			TaskID:     event.TaskID,
			PersonID:   event.PersonKey,
			OccurredAt: event.OccurredAt,
		})
	}
	return out
}

func hydrateItem(ctx context.Context, store Store, q Query, task Task, now time.Time) (Item, error) {
	edges, err := store.ListEdgesBySrc(ctx, q.WorkspaceID, q.AgentID, NodeTask, task.ID)
	if err != nil {
		return Item{}, err
	}
	item := Item{
		Issue:         task.IssueID,
		TaskID:        task.ID,
		Purpose:       task.Purpose,
		Intent:        task.Intent,
		Status:        task.Status,
		LastTouchedAt: task.LastTouchedAt,
		Conversations: []ConversationRef{},
		People:        []PersonRef{},
		WaitingOn:     []WaitingRef{},
	}
	bestRelBoost := 1.0
	convIndex := map[string]int{}
	personIndex := map[string]int{}
	for _, edge := range edges {
		if edge.Status == StatusClosed {
			continue
		}
		switch edge.Rel {
		case RelOutreach, RelTaskScene, RelSpawnedFrom:
			if edge.DstType == NodeScene {
				ref := ConversationRef{
					ConversationID: edge.DstID,
					Kind:           kindFromProps(edge.Props),
					Rel:            edge.Rel,
					Rels:           []string{edge.Rel},
				}
				if i, ok := convIndex[edge.DstID]; ok {
					item.Conversations[i] = mergeConversationRef(item.Conversations[i], ref)
				} else {
					convIndex[edge.DstID] = len(item.Conversations)
					item.Conversations = append(item.Conversations, ref)
				}
				if edge.Rel == RelSpawnedFrom && item.Origin == nil {
					item.Origin = &OriginRef{ConversationID: edge.DstID, Rel: RelSpawnedFrom}
				}
			}
		case RelTaskPerson:
			if edge.DstType == NodePerson {
				person := PersonRef{
					PersonID:    edge.DstID,
					DisplayName: stringProp(edge.Props, "display_name"),
				}
				if i, ok := personIndex[edge.DstID]; ok {
					if item.People[i].DisplayName == "" && person.DisplayName != "" {
						item.People[i] = person
					}
				} else {
					personIndex[edge.DstID] = len(item.People)
					item.People = append(item.People, person)
				}
			}
		case RelWaitingOn:
			ref := WaitingRef{}
			if edge.DstType == NodeScene {
				ref.ConversationID = edge.DstID
			}
			if edge.DstType == NodePerson {
				ref.PersonID = edge.DstID
			}
			item.WaitingOn = append(item.WaitingOn, ref)
		}
		if boost := relBoost(edge.Rel); boost > bestRelBoost {
			bestRelBoost = boost
		}
	}
	item.Score = recencyScore(now.Sub(task.LastTouchedAt)) * statusBoost(task.Status) * bestRelBoost
	return item, nil
}

func recencyScore(age time.Duration) float64 {
	if age < 0 {
		age = 0
	}
	return math.Exp(-float64(age) / float64(RecencyTau))
}

func statusBoost(status string) float64 {
	switch status {
	case StatusWaiting:
		return 1.1
	case StatusDone, StatusCancelled, StatusClosed:
		return 0.2
	default:
		return 1.0
	}
}

func conversationRelRank(rel string) int {
	switch rel {
	case RelOutreach:
		return 3
	case RelTaskScene:
		return 2
	case RelSpawnedFrom:
		return 1
	default:
		return 0
	}
}

func mergeConversationRef(dst, src ConversationRef) ConversationRef {
	if dst.Kind == "" && src.Kind != "" {
		dst.Kind = src.Kind
	}
	dst.Rels = unionRels(dst.Rels, src.Rels)
	if conversationRelRank(src.Rel) > conversationRelRank(dst.Rel) {
		dst.Rel = src.Rel
	}
	return dst
}

func unionRels(a, b []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(a)+len(b))
	for _, group := range [][]string{a, b} {
		for _, rel := range group {
			rel = strings.TrimSpace(rel)
			if rel == "" {
				continue
			}
			if _, ok := seen[rel]; ok {
				continue
			}
			seen[rel] = struct{}{}
			out = append(out, rel)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if conversationRelRank(out[i]) == conversationRelRank(out[j]) {
			return out[i] < out[j]
		}
		return conversationRelRank(out[i]) > conversationRelRank(out[j])
	})
	return out
}

func relBoost(rel string) float64 {
	switch rel {
	case RelWaitingOn:
		return 1.2
	case RelOutreach:
		return 1.0
	default:
		return 1.0
	}
}

func kindFromProps(props map[string]any) string {
	if v := stringProp(props, "kind"); v != "" {
		return v
	}
	return "dm"
}

func stringProp(props map[string]any, key string) string {
	if props == nil {
		return ""
	}
	v, ok := props[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}
