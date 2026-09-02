package assoc

import (
	"context"
	"strings"
	"time"
)

// RecentPersonOutreachScene returns the most recently touched outbound DM cid
// already linked to this person. Used when dws send --user returns only openTaskId.
func (s *Service) RecentPersonOutreachScene(ctx context.Context, workspaceID, agentID, personID string) (string, error) {
	if s == nil || s.store == nil {
		return "", nil
	}
	personID = strings.TrimSpace(personID)
	if workspaceID == "" || agentID == "" || personID == "" {
		return "", nil
	}
	ids := []string{personID}
	if resolved, err := s.store.ResolvePersonKey(ctx, workspaceID, agentID, personID); err == nil && resolved != "" {
		ids = append(ids, resolved)
	}
	since := time.Now().UTC().Add(-7 * 24 * time.Hour)
	var best string
	var bestAt time.Time
	seen := map[string]struct{}{}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		edges, err := s.store.ListEdgesByDst(ctx, workspaceID, agentID, NodePerson, id, since)
		if err != nil {
			return "", err
		}
		for _, pe := range edges {
			if pe.SrcType != NodeTask || pe.Status == StatusClosed {
				continue
			}
			taskEdges, err := s.store.ListEdgesBySrc(ctx, workspaceID, agentID, NodeTask, pe.SrcID)
			if err != nil {
				return "", err
			}
			for _, te := range taskEdges {
				if te.DstType != NodeScene || te.Status == StatusClosed {
					continue
				}
				if te.Rel != RelOutreach && te.Rel != RelWaitingOn {
					continue
				}
				if !ValidSceneID(te.DstID) {
					continue
				}
				if te.LastTouchedAt.After(bestAt) {
					bestAt = te.LastTouchedAt
					best = te.DstID
				}
			}
		}
	}
	return best, nil
}
