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
	if q.ConversationID != "" {
		cidTasks, cidErr := tasksForConversation(ctx, store, q)
		if cidErr != nil {
			return Result{}, cidErr
		}
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
	return Result{Since: q.Since, Until: until, Items: items}, nil
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
	for _, edge := range edges {
		if edge.Status == StatusClosed {
			continue
		}
		switch edge.Rel {
		case RelOutreach, RelTaskScene:
			if edge.DstType == NodeScene {
				ref := ConversationRef{
					ConversationID: edge.DstID,
					Kind:           kindFromProps(edge.Props),
					Rel:            edge.Rel,
				}
				if i, ok := convIndex[edge.DstID]; ok {
					if conversationRelRank(ref.Rel) > conversationRelRank(item.Conversations[i].Rel) {
						item.Conversations[i] = ref
					}
				} else {
					convIndex[edge.DstID] = len(item.Conversations)
					item.Conversations = append(item.Conversations, ref)
				}
			}
		case RelTaskPerson:
			if edge.DstType == NodePerson {
				item.People = append(item.People, PersonRef{
					PersonID:    edge.DstID,
					DisplayName: stringProp(edge.Props, "display_name"),
				})
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
		case RelSpawnedFrom:
			if item.Origin == nil && edge.DstType == NodeScene {
				item.Origin = &OriginRef{ConversationID: edge.DstID, Rel: RelSpawnedFrom}
			}
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
		return 2
	case RelTaskScene:
		return 1
	default:
		return 0
	}
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
