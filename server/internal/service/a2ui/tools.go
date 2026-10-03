package a2ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
)

const (
	ToolAsk     = "a2ui_ask"
	ToolShow    = "a2ui_show"
	ToolApprove = "a2ui_approve"
	ToolRead    = "a2ui_read"
)

// Tool is one parameterized card call. InputSchema is the model-facing
// MCP input schema. Identity, the conversation, and the idempotency key
// stay on Caller and Call; they are not model arguments.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Effect      bool
}

// Definition is the MCP tools/list entry for this tool.
func (t Tool) Definition() map[string]any {
	return map[string]any{
		"name":        t.Name,
		"description": t.Description,
		"inputSchema": t.InputSchema,
	}
}

// Tools returns the card calls a host can mount later. Each call builds a
// fresh schema so a caller cannot change the catalog for the next call.
func Tools() []Tool {
	option := objectSchema([]string{"label"}, map[string]any{
		"label":       stringProp("Choice the person sees."),
		"description": stringProp("Optional explanation under the choice."),
	})
	options := map[string]any{
		"type": "array", "maxItems": maxOptions, "items": option,
		"description": "Choices in display order. Confirm and choose need at least two. Omit for a person picker.",
	}
	return []Tool{
		{
			Name: ToolAsk, Effect: true,
			Description: "Send one option card and return its public id (ask:<uuid>). Set pick_person to ask who, with no options. Set multiple to let them select more than one. Otherwise send a single confirmation. The call returns when the card is sent. Read the same public id after the person answers.",
			InputSchema: objectSchema([]string{"question"}, map[string]any{
				"header":       stringProp("Short title. Defaults to the question."),
				"question":     stringProp("The one question on the card."),
				"options":      options,
				"multiple":     boolProp("Select more than one option."),
				"allow_custom": boolProp("Allow a written answer besides the options. Confirm defaults to yes, choose to no."),
				"pick_person":  boolProp("Open a person picker. Takes no options, multiple, or allow_custom."),
			}),
		},
		{
			Name: ToolShow, Effect: true,
			Description: "Send a chart or a markdown note and return its public id (show:<uuid>). Provide exactly one of chart or markdown. The card has no buttons and is delivered when this call returns.",
			InputSchema: objectSchema(nil, map[string]any{
				"title":    stringProp("Chart title. A note may omit it."),
				"markdown": stringProp("Read-only note. Do not combine with chart."),
				"chart": objectSchema([]string{"points"}, map[string]any{
					"type": map[string]any{
						"type": "string", "enum": []any{"line", "bar", "pie"},
						"description": "line, bar, or pie. Omit for a line chart.",
					},
					"points": map[string]any{
						"type": "array", "minItems": 1, "maxItems": maxPoints,
						"description": "Points in display order.",
						"items": objectSchema([]string{"x", "y"}, map[string]any{
							"x": stringProp("Category or time label."),
							"y": map[string]any{"type": "number", "description": "Finite numeric value."},
						}),
					},
				}),
			}),
		},
		{
			Name: ToolApprove, Effect: true,
			Description: "Send a pending-approval card and return its public id (appr:<uuid>). The first option approves and the second rejects. Omit options to use 同意 and 驳回. The call returns when the card is sent. Read the same public id for approved or rejected.",
			InputSchema: objectSchema(nil, map[string]any{
				"header":       stringProp("Short title. Defaults to 待审批."),
				"question":     stringProp("What they are approving. Defaults to the header."),
				"options":      map[string]any{"type": "array", "minItems": 2, "maxItems": 2, "items": option, "description": "Exactly two choices. The first approves."},
				"allow_custom": boolProp("Allow a written note. Defaults to yes."),
			}),
		},
		{
			Name: ToolRead, Effect: false,
			Description: "Read one card this agent already opened. Pass the public id from ask, show, or approve.",
			InputSchema: objectSchema([]string{"public_id"}, map[string]any{
				"public_id": stringProp("ask:<uuid>, show:<uuid>, or appr:<uuid>."),
			}),
		},
	}
}

// Caller is the host identity for one tool call. The model does not supply it.
type Caller struct {
	WorkspaceID            uuid.UUID
	AgentID                uuid.UUID
	SenderUID              string
	SenderOrgID            string
	SceneID                string
	ConversationID         string
	ThreadID               string
	SourceRef              string
	ReceiverOpenDingTalkID string
	OperatorUID            string
}

// ToolResult is what the model can keep. PublicID is the handle for a later read.
type ToolResult struct {
	PublicID  string   `json:"public_id"`
	SceneID   string   `json:"scene_id,omitempty"`
	MessageID string   `json:"message_id,omitempty"`
	ThreadID  string   `json:"thread_id,omitempty"`
	SourceRef string   `json:"source_ref,omitempty"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Outcome   string   `json:"outcome,omitempty"`
	Selected  []string `json:"selected,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Custom    string   `json:"custom,omitempty"`
}

// Text is a single line a host can place in a tool response.
func (r ToolResult) Text() string {
	parts := []string{"public_id=" + r.PublicID, "scene_id=" + r.SceneID, "message_id=" + r.MessageID, "kind=" + r.Kind, "status=" + r.Status}
	if r.ThreadID != "" {
		parts = append(parts, "thread_id="+r.ThreadID)
	}
	if r.SourceRef != "" {
		parts = append(parts, "source_ref="+r.SourceRef)
	}
	if r.Outcome != "" {
		parts = append(parts, "outcome="+r.Outcome)
	}
	if len(r.Selected) > 0 {
		parts = append(parts, "selected="+strings.Join(r.Selected, ","))
	}
	if len(r.Labels) > 0 {
		parts = append(parts, "labels="+strings.Join(r.Labels, ","))
	}
	if r.Custom != "" {
		parts = append(parts, "custom="+oneLine(r.Custom))
	}
	return strings.Join(parts, " ")
}

// Call runs one catalog tool. callID is the host's idempotency key for a
// card-sending tool and is ignored by a2ui_read. A repeated callID returns
// the card already opened for this agent.
func (s *Service) Call(ctx context.Context, gw Gateway, caller Caller, callID, name string, arguments json.RawMessage) (ToolResult, error) {
	if s == nil || s.store == nil {
		return ToolResult{}, fmt.Errorf("a2ui service is not configured")
	}
	switch name {
	case ToolAsk:
		return s.callAsk(ctx, gw, caller, callID, arguments)
	case ToolShow:
		return s.callShow(ctx, gw, caller, callID, arguments)
	case ToolApprove:
		return s.callApprove(ctx, gw, caller, callID, arguments)
	case ToolRead:
		return s.callRead(ctx, caller, arguments)
	default:
		return ToolResult{}, fmt.Errorf("%w: tool", ErrInvalid)
	}
}

func (s *Service) callAsk(ctx context.Context, gw Gateway, caller Caller, callID string, arguments json.RawMessage) (ToolResult, error) {
	var args struct {
		Header      string      `json:"header"`
		Question    string      `json:"question"`
		Options     []optionArg `json:"options"`
		Multiple    *bool       `json:"multiple"`
		AllowCustom *bool       `json:"allow_custom"`
		PickPerson  *bool       `json:"pick_person"`
	}
	if err := decodeArgs(arguments, &args); err != nil {
		return ToolResult{}, err
	}
	person := flag(args.PickPerson, false)
	multiple := flag(args.Multiple, false)
	if person && (multiple || args.AllowCustom != nil || len(args.Options) > 0) {
		return ToolResult{}, fmt.Errorf("%w: pick_person takes only a question", ErrInvalid)
	}
	kind := KindConfirm
	switch {
	case person:
		kind = KindPerson
	case multiple:
		kind = KindChoose
	}
	return s.openTool(ctx, gw, caller, callID, OpenRequest{
		Kind: kind, Header: args.Header, Question: args.Question,
		Options: optionsFrom(args.Options), AllowCustom: args.AllowCustom,
	})
}

func (s *Service) callShow(ctx context.Context, gw Gateway, caller Caller, callID string, arguments json.RawMessage) (ToolResult, error) {
	var args struct {
		Title    string    `json:"title"`
		Markdown string    `json:"markdown"`
		Chart    *chartArg `json:"chart"`
	}
	if err := decodeArgs(arguments, &args); err != nil {
		return ToolResult{}, err
	}
	hasChart := args.Chart != nil
	hasNote := strings.TrimSpace(args.Markdown) != ""
	if hasChart == hasNote {
		return ToolResult{}, fmt.Errorf("%w: provide a chart or a note", ErrInvalid)
	}
	req := OpenRequest{Header: args.Title, Markdown: args.Markdown, Kind: KindNote}
	if hasChart {
		points := make([]ChartPoint, len(args.Chart.Points))
		for i, point := range args.Chart.Points {
			points[i] = ChartPoint{X: point.X, Y: point.Y}
		}
		req.Kind = KindChart
		req.Chart = &Chart{Type: args.Chart.Type, Points: points}
	}
	return s.openTool(ctx, gw, caller, callID, req)
}

func (s *Service) callApprove(ctx context.Context, gw Gateway, caller Caller, callID string, arguments json.RawMessage) (ToolResult, error) {
	var args struct {
		Header      string      `json:"header"`
		Question    string      `json:"question"`
		Options     []optionArg `json:"options"`
		AllowCustom *bool       `json:"allow_custom"`
	}
	if err := decodeArgs(arguments, &args); err != nil {
		return ToolResult{}, err
	}
	return s.openTool(ctx, gw, caller, callID, OpenRequest{
		Kind: KindApproval, Header: args.Header, Question: args.Question,
		Options: optionsFrom(args.Options), AllowCustom: args.AllowCustom,
	})
}

func (s *Service) callRead(ctx context.Context, caller Caller, arguments json.RawMessage) (ToolResult, error) {
	var args struct {
		PublicID string `json:"public_id"`
	}
	if err := decodeArgs(arguments, &args); err != nil {
		return ToolResult{}, err
	}
	row, err := s.Get(ctx, args.PublicID)
	if err != nil {
		return ToolResult{}, err
	}
	if caller.WorkspaceID != row.WorkspaceID || caller.AgentID != row.AgentID {
		return ToolResult{}, ErrNotFound
	}
	return resultFrom(row), nil
}

func (s *Service) openTool(ctx context.Context, gw Gateway, caller Caller, callID string, req OpenRequest) (ToolResult, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return ToolResult{}, fmt.Errorf("%w: call id", ErrInvalid)
	}
	req.WorkspaceID = caller.WorkspaceID
	req.AgentID = caller.AgentID
	req.SenderUID = caller.SenderUID
	req.SenderOrgID = caller.SenderOrgID
	req.SceneID = caller.SceneID
	req.ConversationID = caller.ConversationID
	req.ThreadID = caller.ThreadID
	req.SourceRef = caller.SourceRef
	req.ReceiverOpenDingTalkID = caller.ReceiverOpenDingTalkID
	req.OperatorUID = caller.OperatorUID
	req.IdempotencyKey = callID
	row, err := s.Open(ctx, gw, req)
	if err != nil {
		return ToolResult{}, err
	}
	return resultFrom(row), nil
}

type optionArg struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type chartArg struct {
	Type   string `json:"type"`
	Points []struct {
		X string  `json:"x"`
		Y float64 `json:"y"`
	} `json:"points"`
}

func optionsFrom(options []optionArg) []Option {
	if len(options) == 0 {
		return nil
	}
	out := make([]Option, len(options))
	for i, option := range options {
		out[i] = Option{Label: option.Label, Description: option.Description}
	}
	return out
}

func resultFrom(row Interaction) ToolResult {
	return ToolResult{
		PublicID:  row.PublicID,
		SceneID:   row.SceneID,
		MessageID: row.MessageID,
		ThreadID:  row.ThreadID,
		SourceRef: row.SourceRef,
		Kind:      string(row.Kind),
		Status:    string(row.Status),
		Outcome:   row.Result.Outcome,
		Selected:  append([]string(nil), row.Result.Selected...),
		Labels:    append([]string(nil), row.Result.Labels...),
		Custom:    row.Result.Custom,
	}
}

func decodeArgs(raw json.RawMessage, dest any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("%w: arguments", ErrInvalid)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: arguments", ErrInvalid)
	}
	return nil
}

func objectSchema(required []string, properties map[string]any) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func boolProp(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func oneLine(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	return clip(text, maxHeader)
}
