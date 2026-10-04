package dingtalkresponse

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service/a2ui"
)

// QuestionCardUpdater is an optional provider capability. Card updates never
// become ordinary response actions that older readers could mistake for sends.
type QuestionCardUpdater interface {
	UpdateQuestionCard(context.Context, ActionInput, string, []string) error
}

// UpdateQuestionCard replaces the already delivered card with a closed
// projection. The caller owns the durable update intent and current authority
// fence; an unknown result may retry this exact update, never send a new card.
func (s *Service) UpdateQuestionCard(ctx context.Context, in ActionInput, bizID string, messages []string) error {
	if s == nil {
		return errors.New("question card update provider is unavailable")
	}
	p, ok := s.provider.(QuestionCardUpdater)
	if !ok {
		return errors.New("question card update provider is unavailable")
	}
	if err := validateQuestionCardUpdate(in, bizID, messages); err != nil {
		return err
	}
	return p.UpdateQuestionCard(ctx, in, bizID, messages)
}

func validateQuestionCardUpdate(in ActionInput, bizID string, messages []string) error {
	for _, id := range []string{in.WorkspaceID, in.AgentID, in.SceneID} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil {
			return errors.New("invalid question card update scope")
		}
	}
	if in.A2UICard == nil || in.DWSUID == "" || in.DWSOrgID == "" || in.ConversationID == "" || strings.TrimSpace(bizID) == "" || len(bizID) > 512 || len(messages) != 1 {
		return errors.New("invalid question card update binding")
	}
	ref, ok := a2ui.ParseRef(in.A2UICard.PublicID)
	if !ok || ref.ID.String() != in.A2UICard.QuestionID {
		return errors.New("invalid question card update reference")
	}
	for _, message := range messages {
		var envelope struct {
			Version string `json:"version"`
			Update  struct {
				SurfaceID  string                       `json:"surfaceId"`
				Components []map[string]json.RawMessage `json:"components"`
			} `json:"updateComponents"`
		}
		var keys map[string]json.RawMessage
		if len(message) > 1<<20 || json.Unmarshal([]byte(message), &envelope) != nil || json.Unmarshal([]byte(message), &keys) != nil || len(keys) != 2 || envelope.Version != "v1.0" || envelope.Update.SurfaceID != ref.SurfaceID() || len(envelope.Update.Components) == 0 {
			return errors.New("invalid question card update projection")
		}
		ids := make(map[string]bool, len(envelope.Update.Components))
		children := make(map[string][]string)
		for _, component := range envelope.Update.Components {
			var name, id string
			if json.Unmarshal(component["component"], &name) != nil || (name != "Text" && name != "Row" && name != "Column") || json.Unmarshal(component["id"], &id) != nil || id == "" {
				return errors.New("question card update is not closed")
			}
			if ids[id] {
				return errors.New("question card update has duplicate components")
			}
			ids[id] = true
			if name == "Text" {
				var literal string
				if json.Unmarshal(component["text"], &literal) != nil {
					return errors.New("question card update requires static text")
				}
			} else {
				var refs []string
				if json.Unmarshal(component["children"], &refs) != nil || len(refs) == 0 {
					return errors.New("question card update requires a static tree")
				}
				children[id] = refs
			}
			// Closed cards consist only of static display primitives. The
			// provider boundary rejects a projection that could reopen input.
			for kind, body := range component {
				switch kind {
				case "id":
				case "component":
					var name string
					if json.Unmarshal(body, &name) != nil || (name != "Text" && name != "Row" && name != "Column") {
						return errors.New("question card update is not closed")
					}
				case "text", "children", "variant", "justify", "align", "weight", "catalogId", "gap", "bold", "colorToken", "maxLine":
				default:
					return errors.New("question card update has an unsupported field")
				}
			}
		}
		// Renderers may retain old component ids. Every child of the new root
		// must belong to this replacement, so no old button remains reachable.
		visited := make(map[string]int)
		var check func(string) bool
		check = func(id string) bool {
			if !ids[id] || visited[id] == 1 {
				return false
			}
			if visited[id] == 2 {
				return true
			}
			visited[id] = 1
			for _, child := range children[id] {
				if !check(child) {
					return false
				}
			}
			visited[id] = 2
			return true
		}
		if !check("root") {
			return errors.New("question card update references a retained component")
		}
	}
	return nil
}
