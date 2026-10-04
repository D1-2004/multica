package a2ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// Gateway sends and finishes cards for one DingTalk identity. *dws.MessageService
// implements it. Finish is best-effort after the click is already stored.
type Gateway interface {
	SendA2UI(ctx context.Context, req dws.A2UISend) (dws.A2UIReceipt, error)
	FinishA2UI(ctx context.Context, bizID, surfaceID string) error
}

type store interface {
	Insert(ctx context.Context, row Interaction) error
	GetByPublicID(ctx context.Context, publicID string) (Interaction, error)
	GetByIdempotency(ctx context.Context, agentID uuid.UUID, key string) (Interaction, error)
	MarkSent(ctx context.Context, id uuid.UUID, bizID, messageID, conversationID string, status Status) error
	MarkFailed(ctx context.Context, id uuid.UUID) error
	Resolve(ctx context.Context, id uuid.UUID, eventID, operator string, status Status, result []byte) (bool, error)
	ListByMessage(ctx context.Context, agentID uuid.UUID, sceneID, messageID string) ([]Interaction, error)
	GetByMessage(ctx context.Context, agentID uuid.UUID, messageID string) (Interaction, error)
}

// Service opens cards and applies the clicks that name their public ids.
type Service struct {
	store store
}

func newService(st store) *Service { return &Service{store: st} }

// Open validates the request, records it, and sends the card. A repeated
// idempotency key returns the card already sent. A previous failed send is
// tried again. An open row whose send outcome was never recorded is returned
// as-is, so a lost response is not sent a second time.
func (s *Service) Open(ctx context.Context, gw Gateway, req OpenRequest) (Interaction, error) {
	if s == nil || s.store == nil {
		return Interaction{}, errors.New("a2ui service is not configured")
	}
	if gw == nil {
		return Interaction{}, fmt.Errorf("%w: gateway", ErrInvalid)
	}
	normalized, err := normalize(req)
	if err != nil {
		return Interaction{}, err
	}
	if normalized.IdempotencyKey != "" {
		existing, err := s.store.GetByIdempotency(ctx, normalized.AgentID, normalized.IdempotencyKey)
		switch {
		case err == nil:
			return s.useExisting(ctx, gw, existing)
		case !errors.Is(err, ErrNotFound):
			return Interaction{}, err
		}
	}
	normalized.ID = uuid.New()
	ref := Ref{Family: normalized.Kind.Family(), ID: normalized.ID}
	normalized.PublicID = ref.PublicID()
	normalized.spec.SurfaceID = ref.SurfaceID()
	normalized.Status = StatusOpen
	if err := s.store.Insert(ctx, normalized); err != nil {
		if errors.Is(err, ErrConflict) && normalized.IdempotencyKey != "" {
			existing, getErr := s.store.GetByIdempotency(ctx, normalized.AgentID, normalized.IdempotencyKey)
			if getErr != nil {
				return Interaction{}, getErr
			}
			return s.useExisting(ctx, gw, existing)
		}
		return Interaction{}, err
	}
	return s.send(ctx, gw, normalized)
}

func (s *Service) useExisting(ctx context.Context, gw Gateway, row Interaction) (Interaction, error) {
	if row.CardBizID != "" {
		return row, nil
	}
	if row.Status == StatusFailed {
		return s.send(ctx, gw, row)
	}
	return row, nil
}

func (s *Service) send(ctx context.Context, gw Gateway, row Interaction) (Interaction, error) {
	messages, err := projectCard(row.PublicID, row.spec.SurfaceID, row.Kind, row.Header, row.Question, row.spec)
	if err != nil {
		return row, err
	}
	receipt, err := gw.SendA2UI(ctx, dws.A2UISend{
		Target: dws.Target{
			ConversationID:     row.ConversationID,
			UserOpenDingTalkID: row.spec.ReceiverOpenDingTalkID,
		},
		Messages:  messages,
		Summary:   summaryOf(row.Header, row.Question, row.spec.Markdown),
		BizCardID: row.ID.String(),
		RequestID: uuid.NewString(),
	})
	if err != nil || strings.TrimSpace(receipt.BizID) == "" {
		if markErr := s.store.MarkFailed(ctx, row.ID); markErr != nil {
			slog.Warn("a2ui failed send was not recorded", "event", "a2ui_mark_failed", "public_id", row.PublicID, "error", markErr)
		}
		row.Status = StatusFailed
		if err == nil {
			err = fmt.Errorf("%w: send was not confirmed", ErrInvalid)
		}
		return row, err
	}
	row.CardBizID = strings.TrimSpace(receipt.BizID)
	if id := strings.TrimSpace(receipt.MessageID); id != "" {
		row.MessageID = id
	}
	if id := strings.TrimSpace(receipt.ConversationID); id != "" {
		row.ConversationID = id
	}
	row.Status = StatusOpen
	if !row.Kind.Waits() {
		row.Status = StatusDelivered
	}
	if err := s.store.MarkSent(ctx, row.ID, row.CardBizID, row.MessageID, row.ConversationID, row.Status); err != nil {
		return row, err
	}
	return row, nil
}

// Accept applies one native card event. Unknown cards, coordinator cards and
// clicks that fail the sender fence are acknowledged. A database error is
// returned so the stream delivers the event again. The first answer wins.
// Finishing the visible card happens after the row is saved; a finish failure
// is logged and does not fail the event.
func (s *Service) Accept(ctx context.Context, actor Actor, line []byte, finish func(context.Context, string, string) error) error {
	if s == nil || s.store == nil {
		return errors.New("a2ui service is not configured")
	}
	got, err := parseClick(line)
	if err != nil {
		if errors.Is(err, errForeign) || errors.Is(err, errMalformed) {
			slog.Warn("a2ui card event ignored", "event", "a2ui_click_ignored", "reason", err.Error())
			return nil
		}
		return err
	}
	row, err := s.store.GetByPublicID(ctx, got.PublicID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !allows(actor, row, got) {
		slog.Warn("a2ui card event dropped", "event", "a2ui_click_dropped", "public_id", row.PublicID)
		return nil
	}
	if row.Status != StatusOpen {
		return nil
	}
	status, result := decide(row, got)
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	won, err := s.store.Resolve(ctx, row.ID, got.EventID, got.Operator, status, body)
	if errors.Is(err, ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	if !won {
		return nil
	}
	if finish != nil && row.Kind.Waits() && row.CardBizID != "" {
		surface := row.spec.SurfaceID
		if surface == "" {
			if ref, ok := ParseRef(row.PublicID); ok {
				surface = ref.SurfaceID()
			}
		}
		if ferr := finish(ctx, row.CardBizID, surface); ferr != nil {
			slog.Warn("a2ui card finish failed", "event", "a2ui_finish_failed", "public_id", row.PublicID, "error", ferr)
		}
	}
	return nil
}

// ByMessage returns the card whose send receipt carried this message id.
// After a click, Result is the reply that message received.
func (s *Service) ByMessage(ctx context.Context, agentID uuid.UUID, messageID string) (Interaction, error) {
	if s == nil || s.store == nil {
		return Interaction{}, errors.New("a2ui service is not configured")
	}
	messageID = strings.TrimSpace(messageID)
	if agentID == uuid.Nil || messageID == "" {
		return Interaction{}, fmt.Errorf("%w: message", ErrInvalid)
	}
	return s.store.GetByMessage(ctx, agentID, messageID)
}

// ForMessage returns cards this agent sent in one scene with this card message id.
func (s *Service) ForMessage(ctx context.Context, agentID uuid.UUID, sceneID, messageID string) ([]Interaction, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("a2ui service is not configured")
	}
	sceneID = strings.TrimSpace(sceneID)
	messageID = strings.TrimSpace(messageID)
	if agentID == uuid.Nil || sceneID == "" || messageID == "" {
		return nil, fmt.Errorf("%w: scene message", ErrInvalid)
	}
	return s.store.ListByMessage(ctx, agentID, sceneID, messageID)
}

// Get returns the interaction for a public id such as appr:<uuid>.
func (s *Service) Get(ctx context.Context, publicID string) (Interaction, error) {
	if s == nil || s.store == nil {
		return Interaction{}, errors.New("a2ui service is not configured")
	}
	publicID = strings.TrimSpace(publicID)
	if _, ok := ParseRef(publicID); !ok {
		return Interaction{}, fmt.Errorf("%w: public id", ErrInvalid)
	}
	return s.store.GetByPublicID(ctx, publicID)
}

func allows(actor Actor, row Interaction, got click) bool {
	if actor.AgentID == uuid.Nil || actor.UID == "" || actor.OrgID == "" {
		return false
	}
	if actor.AgentID != row.AgentID || actor.UID != row.SenderUID || actor.OrgID != row.SenderOrgID {
		return false
	}
	if orgConflicts(row.SenderOrgID, got.CorpID) {
		return false
	}
	if row.ConversationID != "" && got.ConversationID != "" && row.ConversationID != got.ConversationID {
		return false
	}
	if row.spec.OperatorUID != "" && got.Operator != row.spec.OperatorUID {
		return false
	}
	if row.spec.EmployeeCompact && (row.Kind == KindConfirm || row.Kind == KindChoose) {
		if got.Outcome == "skipped" {
			return true
		}
		if got.Outcome != "answered" || (!row.spec.Multiple && len(got.Selected) > 1) || (len(got.Selected) == 0 && got.Custom == "") {
			return false
		}
		seen := map[string]bool{}
		for _, id := range got.Selected {
			if seen[id] {
				return false
			}
			seen[id] = true
			found := false
			for _, option := range row.spec.Options {
				if option.ID == id {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

// orgConflicts compares two organization ids in the same identifier space.
// A numeric org id and a ding corp id name the same organization differently,
// so that pair is not a mismatch. The subscription actor is the real fence.
func orgConflicts(stored, eventCorp string) bool {
	stored = strings.TrimSpace(stored)
	eventCorp = strings.TrimSpace(eventCorp)
	if stored == "" || eventCorp == "" || stored == eventCorp {
		return false
	}
	if strings.HasPrefix(stored, "ding") != strings.HasPrefix(eventCorp, "ding") {
		return false
	}
	return true
}

func decide(row Interaction, got click) (Status, Result) {
	result := Result{Selected: got.Selected, Labels: labelsFor(row.spec.Options, got.Selected), Custom: got.Custom}
	if result.Selected == nil {
		result.Selected = []string{}
	}
	if result.Labels == nil {
		result.Labels = []string{}
	}
	if strings.EqualFold(strings.TrimSpace(got.Outcome), "skipped") {
		result.Outcome = string(StatusSkipped)
		return StatusSkipped, result
	}
	if row.Kind == KindApproval && len(row.spec.Options) >= 2 {
		approve, reject := row.spec.Options[0].ID, row.spec.Options[1].ID
		hasApprove, hasReject := contains(got.Selected, approve), contains(got.Selected, reject)
		switch {
		case hasApprove && !hasReject:
			result.Outcome = string(StatusApproved)
			return StatusApproved, result
		case hasReject && !hasApprove:
			result.Outcome = string(StatusRejected)
			return StatusRejected, result
		}
	}
	if len(got.Selected) == 0 && got.Custom == "" {
		result.Outcome = string(StatusSkipped)
		return StatusSkipped, result
	}
	result.Outcome = string(StatusAnswered)
	return StatusAnswered, result
}

func labelsFor(options []storedOption, selected []string) []string {
	byID := make(map[string]string, len(options))
	for _, option := range options {
		byID[option.ID] = option.Label
	}
	labels := make([]string, 0, len(selected))
	for _, id := range selected {
		if label, ok := byID[id]; ok {
			labels = append(labels, label)
		} else {
			labels = append(labels, id)
		}
	}
	return labels
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func normalize(req OpenRequest) (Interaction, error) {
	req.SenderUID = strings.TrimSpace(req.SenderUID)
	req.SenderOrgID = strings.TrimSpace(req.SenderOrgID)
	req.SceneID = strings.TrimSpace(req.SceneID)
	req.ConversationID = strings.TrimSpace(req.ConversationID)
	req.ThreadID = strings.TrimSpace(req.ThreadID)
	req.SourceRef = strings.TrimSpace(req.SourceRef)
	req.ReceiverOpenDingTalkID = strings.TrimSpace(req.ReceiverOpenDingTalkID)
	req.Header = strings.TrimSpace(req.Header)
	req.Question = strings.TrimSpace(req.Question)
	req.Markdown = strings.TrimSpace(req.Markdown)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.OperatorUID = strings.TrimSpace(req.OperatorUID)
	if req.WorkspaceID == uuid.Nil || req.AgentID == uuid.Nil {
		return Interaction{}, fmt.Errorf("%w: workspace and agent are required", ErrInvalid)
	}
	if req.SenderUID == "" || req.SenderOrgID == "" {
		return Interaction{}, fmt.Errorf("%w: sender is required", ErrInvalid)
	}
	if tooLong(req.SenderUID) || tooLong(req.SenderOrgID) || tooLong(req.SceneID) || tooLong(req.ThreadID) || tooLong(req.SourceRef) || tooLong(req.IdempotencyKey) || tooLong(req.OperatorUID) {
		return Interaction{}, fmt.Errorf("%w: id is too long", ErrInvalid)
	}
	if req.Kind.Waits() && req.SceneID == "" {
		return Interaction{}, fmt.Errorf("%w: scene is required", ErrInvalid)
	}
	targets := 0
	if req.ConversationID != "" {
		targets++
	}
	if req.ReceiverOpenDingTalkID != "" {
		targets++
	}
	if targets != 1 {
		return Interaction{}, fmt.Errorf("%w: exactly one target", ErrInvalid)
	}
	spec := storedRequest{
		ReceiverOpenDingTalkID: req.ReceiverOpenDingTalkID,
		OperatorUID:            req.OperatorUID,
		Markdown:               req.Markdown,
		EmployeeCompact:        req.EmployeeCompact,
		SourceQuote:            clip(strings.Join(strings.Fields(req.SourceQuote), " "), 80),
	}
	switch req.Kind {
	case KindConfirm, KindChoose:
		if req.Question == "" {
			return Interaction{}, fmt.Errorf("%w: question is required", ErrInvalid)
		}
		spec.Multiple = req.Kind == KindChoose
		spec.AllowCustom = flag(req.AllowCustom, req.Kind == KindConfirm)
		options, err := normalizeOptions(req.Options, 2)
		if err != nil {
			return Interaction{}, err
		}
		spec.Options = options
	case KindPerson:
		if req.Question == "" {
			return Interaction{}, fmt.Errorf("%w: question is required", ErrInvalid)
		}
		if len(req.Options) != 0 {
			return Interaction{}, fmt.Errorf("%w: person takes no options", ErrInvalid)
		}
	case KindApproval:
		if req.Header == "" {
			req.Header = "待审批"
		}
		if req.Question == "" {
			req.Question = req.Header
		}
		options := req.Options
		if len(options) == 0 {
			options = []Option{{Label: "同意"}, {Label: "驳回"}}
		}
		if len(options) != 2 {
			return Interaction{}, fmt.Errorf("%w: approval needs exactly two options", ErrInvalid)
		}
		normalized, err := normalizeOptions(options, 2)
		if err != nil {
			return Interaction{}, err
		}
		spec.Options = normalized
		spec.AllowCustom = flag(req.AllowCustom, true)
	case KindChart:
		if req.Header == "" {
			return Interaction{}, fmt.Errorf("%w: chart needs a title", ErrInvalid)
		}
		if req.Chart == nil {
			return Interaction{}, fmt.Errorf("%w: chart needs points", ErrInvalid)
		}
		chart, err := normalizeChart(*req.Chart)
		if err != nil {
			return Interaction{}, err
		}
		spec.Chart = &chart
	case KindNote:
		if req.Markdown == "" {
			return Interaction{}, fmt.Errorf("%w: note needs markdown", ErrInvalid)
		}
		if len([]rune(req.Markdown)) > maxMarkdown {
			return Interaction{}, fmt.Errorf("%w: markdown is too long", ErrInvalid)
		}
	default:
		return Interaction{}, fmt.Errorf("%w: kind", ErrInvalid)
	}
	if req.Kind.Waits() {
		if len([]rune(req.Question)) > maxQuestion {
			return Interaction{}, fmt.Errorf("%w: question is too long", ErrInvalid)
		}
		if req.Header == "" {
			req.Header = clip(req.Question, maxHeader)
		}
	}
	if len([]rune(req.Header)) > maxHeader {
		return Interaction{}, fmt.Errorf("%w: header is too long", ErrInvalid)
	}
	return Interaction{
		WorkspaceID: req.WorkspaceID, AgentID: req.AgentID,
		SenderUID: req.SenderUID, SenderOrgID: req.SenderOrgID,
		SceneID: req.SceneID, ConversationID: req.ConversationID,
		ThreadID: req.ThreadID, SourceRef: req.SourceRef,
		Kind: req.Kind, Header: req.Header, Question: req.Question,
		IdempotencyKey: req.IdempotencyKey, spec: spec,
	}, nil
}

func normalizeOptions(options []Option, min int) ([]storedOption, error) {
	if len(options) < min || len(options) > maxOptions {
		return nil, fmt.Errorf("%w: option count", ErrInvalid)
	}
	out := make([]storedOption, 0, len(options))
	for i, option := range options {
		label := strings.TrimSpace(option.Label)
		description := strings.TrimSpace(option.Description)
		if label == "" || len([]rune(label)) > maxLabel || len([]rune(description)) > maxDescription {
			return nil, fmt.Errorf("%w: option label", ErrInvalid)
		}
		stored := storedOption{ID: fmt.Sprintf("o%d", i), Label: label}
		if description != "" {
			stored.Description = description
		}
		out = append(out, stored)
	}
	return out, nil
}

func normalizeChart(chart Chart) (storedChart, error) {
	kind := strings.TrimSpace(chart.Type)
	if kind == "" {
		kind = "line"
	}
	switch kind {
	case "line", "bar", "pie":
	default:
		return storedChart{}, fmt.Errorf("%w: chart type", ErrInvalid)
	}
	if len(chart.Points) == 0 || len(chart.Points) > maxPoints {
		return storedChart{}, fmt.Errorf("%w: chart points", ErrInvalid)
	}
	points := make([]ChartPoint, 0, len(chart.Points))
	for _, point := range chart.Points {
		point.X = strings.TrimSpace(point.X)
		if point.X == "" || len([]rune(point.X)) > maxHeader || math.IsNaN(point.Y) || math.IsInf(point.Y, 0) {
			return storedChart{}, fmt.Errorf("%w: chart point", ErrInvalid)
		}
		points = append(points, point)
	}
	return storedChart{Type: kind, Points: points}, nil
}

func flag(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func summaryOf(header, question, markdown string) string {
	for _, text := range []string{header, question, markdown} {
		text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
		if text != "" {
			return clip(text, maxHeader)
		}
	}
	return "卡片"
}

func clip(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n])
}

func tooLong(text string) bool { return len([]rune(text)) > maxID }
