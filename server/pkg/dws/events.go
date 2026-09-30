package dws

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// EventService manages the identity's personal event subscriptions and
// issues stream tickets. Subscriptions live on DingTalk's side and outlive
// any connection; the events package keeps a connection open and reconciles
// subscriptions against the product's configuration.
type EventService struct{ c *Client }

// DefaultSourceID is the event source dws uses for personal subscriptions.
const DefaultSourceID = "open"

// Personal event keys (dingtalk-workspace-cli internal/event/personal).
const (
	EventIMAt              = "user_im_message_receive_at"
	EventIMSingleChat      = "user_im_message_receive_o2o"
	EventIMGroup           = "user_im_message_receive_group"
	EventIMFromUser        = "user_im_message_receive_user"
	EventIMAllSingleChats  = "user_im_message_receive_o2o_all"
	EventIMAllGroups       = "user_im_message_receive_group_all"
	EventIMReadSingleChat  = "user_im_message_read_o2o"
	EventIMReadGroup       = "user_im_message_read_group"
	EventIMRecallSingle    = "user_im_message_recall_o2o"
	EventIMRecallGroup     = "user_im_message_recall_group"
	EventIMReactionSingle  = "user_im_message_reaction_o2o"
	EventIMReactionGroup   = "user_im_message_reaction_group"
	EventGroupUpdated      = "user_im_group_updated"
	EventGroupMemberAdded  = "user_im_group_member_added"
	EventGroupMemberExited = "user_im_group_member_exited"
	EventGroupDisbanded    = "user_im_group_disbanded"
	EventApprovalTaskNew   = "user_oa_approval_task_created"
	EventApprovalTaskDone  = "user_oa_approval_task_finished"
	EventApprovalTaskMoved = "user_oa_approval_task_redirected"
	EventApprovalStarted   = "user_oa_approval_instance_started"
	EventApprovalCC        = "user_oa_approval_instance_cc"
	EventApprovalStopped   = "user_oa_approval_instance_terminated"
	EventApprovalFinished  = "user_oa_approval_instance_finished"
	EventVoIPInvite        = "user_voip_call_receive_invite"
	EventTodoCreated       = "user_todo_task_create"
	EventTodoUpdated       = "user_todo_task_update"
	EventTodoDeleted       = "user_todo_task_delete"
	EventCardAction        = "user_card_action_triggered"
)

// SubscriptionSpec is one configured subscription: an event key, the scope
// its rule needs (see EventDefinition.Scope), and optionally a filter that
// DingTalk applies before delivering.
type SubscriptionSpec struct {
	EventKey string `json:"eventKey"`
	// ConversationID scopes group rules (…_group, user_im_group_*).
	ConversationID string `json:"conversationId,omitempty"`
	// TargetOpenDingTalkID or TargetUserID scopes single-chat and sender
	// rules (…_o2o, …_receive_user).
	TargetOpenDingTalkID string `json:"targetOpenDingTalkId,omitempty"`
	TargetUserID         string `json:"targetUserId,omitempty"`
	// RoleTypes scopes todo rules (creator, executor, participant; default
	// all three). Order and repeats do not matter.
	RoleTypes []string `json:"roleTypes,omitempty"`
	// Filter narrows message receive events on DingTalk's side (dws
	// --filter-json): a condition {"field","op","value"} or {"and"|"or":[…]}.
	// Fields may use the aliases content, conversation_id, sender and
	// sender_open_dingtalk_id for their payload.body paths.
	Filter json.RawMessage `json:"filter,omitempty"`
	// Keywords keeps message receive events whose content contains any of
	// them (dws --query). Combined with Filter by "and".
	Keywords []string `json:"keywords,omitempty"`
	// TTLSeconds lets DingTalk expire the subscription, a safety net for one
	// a host may forget. It applies when the subscription is created and is
	// not part of its identity; a reconciled spec is recreated after expiry.
	TTLSeconds int64 `json:"ttlSeconds,omitempty"`
	// Name labels the subscription on DingTalk's side.
	Name string `json:"name,omitempty"`
}

var todoRoles = []string{"creator", "executor", "participant"}

// Rule returns the rule type and parameters for the spec, validating that
// the scope fits the key's definition.
func (s SubscriptionSpec) Rule() (string, map[string]any, error) {
	def, ok := LookupEvent(s.EventKey)
	if !ok {
		return "", nil, invalid("unknown event key " + s.EventKey)
	}
	if !def.Available() {
		return "", nil, invalid(s.EventKey + " is not available for subscription yet")
	}
	target := s.TargetOpenDingTalkID != "" || s.TargetUserID != ""
	switch def.Scope() {
	case ScopeRoles:
		if s.ConversationID != "" || target {
			return "", nil, invalid(s.EventKey + " takes role types only")
		}
		selected := map[string]bool{}
		for _, r := range s.RoleTypes {
			r = strings.TrimSpace(r)
			if r != "creator" && r != "executor" && r != "participant" {
				return "", nil, invalid("unknown todo role " + r)
			}
			selected[r] = true
		}
		// Canonical order, so the same roles always name the same rule.
		roles := make([]string, 0, len(todoRoles))
		for _, r := range todoRoles {
			if selected[r] || len(selected) == 0 {
				roles = append(roles, r)
			}
		}
		return def.RuleType, map[string]any{"roleTypes": roles}, nil
	case ScopeGroup:
		if s.ConversationID == "" || target || len(s.RoleTypes) > 0 {
			return "", nil, invalid(s.EventKey + " needs a conversationId only")
		}
		return def.RuleType, map[string]any{"openConversationId": s.ConversationID}, nil
	case ScopeTarget:
		if s.ConversationID != "" || len(s.RoleTypes) > 0 || (s.TargetOpenDingTalkID != "") == (s.TargetUserID != "") {
			return "", nil, invalid(s.EventKey + " needs exactly one of targetOpenDingTalkId or targetUserId")
		}
		if s.TargetOpenDingTalkID != "" {
			return def.RuleType, map[string]any{"targetUid": s.TargetOpenDingTalkID, "targetUidType": "openDingtalkId"}, nil
		}
		return def.RuleType, map[string]any{"targetUid": s.TargetUserID, "targetUidType": "staffId"}, nil
	case ScopeNone:
		if s.ConversationID != "" || target || len(s.RoleTypes) > 0 {
			return "", nil, invalid(s.EventKey + " takes no scope")
		}
		return def.RuleType, map[string]any{}, nil
	default:
		return "", nil, invalid(s.EventKey + " uses rule type " + def.RuleType + ", which this SDK does not know yet")
	}
}

// filterAliases are the payload paths dws lets a filter name briefly.
var filterAliases = map[string]string{
	"content":                 "payload.body.content",
	"conversation_id":         "payload.body.openConversationId",
	"sender":                  "payload.body.sender",
	"sender_open_dingtalk_id": "payload.body.senderOpenDingTalkId",
}

// FilterRule returns the filter DingTalk applies and its canonical JSON,
// both empty without Filter or Keywords, the same way dws builds them.
func (s SubscriptionSpec) FilterRule() (any, string, error) {
	var parts []any
	if len(bytes.TrimSpace(s.Filter)) > 0 {
		var v any
		if err := json.Unmarshal(s.Filter, &v); err != nil {
			return nil, "", invalid("filter must be valid JSON: " + err.Error())
		}
		if _, ok := v.(map[string]any); !ok {
			return nil, "", invalid("filter must be a JSON object")
		}
		parts = append(parts, expandFilterAliases(v))
	}
	var keywords []any
	for _, k := range s.Keywords {
		if k = strings.TrimSpace(k); k != "" {
			keywords = append(keywords, k)
		}
	}
	if len(keywords) > 0 {
		parts = append(parts, map[string]any{"field": "payload.body.content", "op": "contains_any", "value": keywords})
	}
	if len(parts) == 0 {
		return nil, "", nil
	}
	if def, ok := LookupEvent(s.EventKey); ok && !def.SupportsFilter() {
		return nil, "", invalid(s.EventKey + " carries no message body to filter")
	}
	var rule any = parts[0]
	if len(parts) > 1 {
		rule = map[string]any{"and": parts}
	}
	canon, err := json.Marshal(rule) // map keys marshal sorted
	if err != nil {
		return nil, "", invalid("filter: " + err.Error())
	}
	return rule, string(canon), nil
}

func expandFilterAliases(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, value := range x {
			if field, ok := value.(string); ok && k == "field" {
				if path, ok := filterAliases[field]; ok {
					value = path
				}
			} else {
				value = expandFilterAliases(value)
			}
			out[k] = value
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, value := range x {
			out[i] = expandFilterAliases(value)
		}
		return out
	default:
		return v
	}
}

// Validate reports whether the spec can be subscribed.
func (s SubscriptionSpec) Validate() error {
	if _, _, err := s.Rule(); err != nil {
		return err
	}
	if s.TTLSeconds < 0 {
		return invalid("ttlSeconds must not be negative")
	}
	_, _, err := s.FilterRule()
	return err
}

// Fingerprint identifies the spec's rule and filter; equal specs share it,
// and so does their idempotency key on DingTalk's side. Specs without a
// filter keep the fingerprint they had before filters existed.
func (s SubscriptionSpec) Fingerprint() string {
	if s.Validate() != nil {
		return ""
	}
	ruleType, param, _ := s.Rule()
	_, filter, _ := s.FilterRule()
	raw, _ := json.Marshal(param) // map keys marshal sorted
	key := s.EventKey + "\x00" + ruleType + "\x00" + string(raw)
	if filter != "" {
		key += "\x00" + filter
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// Subscription is a subscription on DingTalk's side.
type Subscription struct {
	ID          string `json:"subId"`
	EventKey    string `json:"eventKey"`
	RuleType    string `json:"ruleType,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	// Status is 1 active, 2 paused, 3 deleted.
	Status int `json:"status,omitempty"`
	// Spec is what the subscription was created from (set by Subscribe).
	// Subscribing the same spec again returns the same ID: DWS dedupes on
	// the idempotency key, even after a cancel (measured on staging).
	Spec *SubscriptionSpec `json:"spec,omitempty"`
}

// Subscribe creates the subscription. The idempotency key derives from the
// spec, so retrying an ambiguous create does not duplicate it.
func (s *EventService) Subscribe(ctx context.Context, spec SubscriptionSpec) (Subscription, error) {
	if err := spec.Validate(); err != nil {
		return Subscription{}, err
	}
	ruleType, param, _ := spec.Rule()
	filter, _, _ := spec.FilterRule()
	ruleParam, _ := json.Marshal(param)
	fp := spec.Fingerprint()
	ext := map[string]any{"ruleType": ruleType, "idempotencyKey": "dws-for-tag-" + fp}
	if spec.Name != "" {
		ext["name"] = spec.Name
	}
	if filter != nil {
		ext["filter"] = filter
	}
	body := map[string]any{
		"clientId": "", "sourceId": DefaultSourceID, "eventKey": spec.EventKey,
		"filterRule": string(ruleParam), "deliveryPref": "realtime", "ext": ext,
	}
	if spec.TTLSeconds > 0 {
		body["expiresAt"] = s.c.cfg.now().UTC().Add(time.Duration(spec.TTLSeconds) * time.Second).Format(time.RFC3339)
	}
	var raw json.RawMessage
	err := s.do(ctx, http.MethodPost, "/dws/subscription/user", nil, body, &raw)
	if err != nil {
		return Subscription{}, err
	}
	// The result is ["<subId>"] or an object carrying subId.
	var ids []string
	var obj struct {
		SubID string `json:"subId"`
	}
	id := ""
	if json.Unmarshal(raw, &ids) == nil && len(ids) > 0 {
		id = ids[0]
	} else if json.Unmarshal(raw, &obj) == nil {
		id = obj.SubID
	}
	if id == "" {
		return Subscription{}, errors.New("dws: subscription create returned no subId")
	}
	return Subscription{ID: id, EventKey: spec.EventKey, RuleType: ruleType, Fingerprint: fp, Status: 1, Spec: &spec}, nil
}

// List returns the identity's subscriptions for this source.
func (s *EventService) List(ctx context.Context) ([]Subscription, error) {
	var all []Subscription
	for page := 1; page <= 20; page++ {
		var raw json.RawMessage
		q := url.Values{"sourceId": {DefaultSourceID}, "pageNo": {fmt.Sprint(page)}, "pageSize": {"100"}}
		if err := s.do(ctx, http.MethodGet, "/dws/event/sublist", q, nil, &raw); err != nil {
			return nil, err
		}
		var pg struct {
			Total int            `json:"total"`
			Items []Subscription `json:"items"`
			List  []Subscription `json:"list"`
		}
		if err := json.Unmarshal(raw, &pg); err != nil {
			return nil, fmt.Errorf("dws: decode subscriptions: %w", err)
		}
		items := append(pg.Items, pg.List...)
		all = append(all, items...)
		if len(items) < 100 || (pg.Total > 0 && len(all) >= pg.Total) {
			break
		}
	}
	return all, nil
}

// Cancel deletes a subscription.
func (s *EventService) Cancel(ctx context.Context, subID string) error {
	if subID == "" {
		return invalid("cancel needs a subscription id")
	}
	return s.do(ctx, http.MethodPost, "/dws/subscription/cancel", nil, map[string]string{"subId": subID}, nil)
}

// Ticket is a single-use credential for one stream connection.
type Ticket struct {
	Endpoint string
	Ticket   string
}

// URL is the WebSocket address to dial.
func (t Ticket) URL() string {
	if strings.Contains(t.Endpoint, "ticket=") {
		return t.Endpoint
	}
	sep := "?"
	if strings.Contains(t.Endpoint, "?") {
		sep = "&"
	}
	return t.Endpoint + sep + "ticket=" + url.QueryEscape(t.Ticket)
}

// Ticket requests a stream ticket. Fetch a new one for every connection.
func (s *EventService) Ticket(ctx context.Context) (Ticket, error) {
	var raw json.RawMessage
	err := s.do(ctx, http.MethodPost, "/stream/connections/ticket", nil,
		map[string]string{"sourceId": DefaultSourceID, "mode": "normal"}, &raw)
	if err != nil {
		return Ticket{}, err
	}
	// Seen shapes: {endpoint,ticket}, {data:{…}}, and the envelope's result.
	var t struct {
		Endpoint string `json:"endpoint"`
		Ticket   string `json:"ticket"`
		Data     *struct {
			Endpoint string `json:"endpoint"`
			Ticket   string `json:"ticket"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &t)
	if t.Endpoint == "" && t.Data != nil {
		t.Endpoint, t.Ticket = t.Data.Endpoint, t.Data.Ticket
	}
	if t.Endpoint == "" || t.Ticket == "" {
		return Ticket{}, errors.New("dws: stream ticket response has no endpoint or ticket")
	}
	return Ticket{Endpoint: t.Endpoint, Ticket: t.Ticket}, nil
}

// controlPlaneTimeout bounds one control-plane call whatever HTTP client
// the Config carries.
const controlPlaneTimeout = 20 * time.Second

type controlEnvelope struct {
	Success   *bool           `json:"success"`
	Result    json.RawMessage `json:"result"`
	ErrorCode string          `json:"errorCode"`
	ErrorMsg  string          `json:"errorMsg"`
}

// do calls the DWS event control plane on the MCP host. It retries once on
// a token rejection (HTTP 401/403 or an auth business code), like Call.
func (s *EventService) do(ctx context.Context, method, path string, q url.Values, body any, out *json.RawMessage) error {
	ctx, cancel := context.WithTimeout(ctx, controlPlaneTimeout)
	defer cancel()
	if err := s.c.mu.lock(ctx); err != nil {
		return fmt.Errorf("dws: events %s: %w", path, err)
	}
	clientID := s.c.token.ClientID
	s.c.mu.unlock()
	if clientID == "" {
		return invalid("events need the token's client id")
	}
	if b, ok := body.(map[string]any); ok && b["clientId"] == "" {
		b["clientId"] = clientID
	}
	if q != nil {
		q.Set("clientId", clientID)
	}
	token, err := s.c.accessToken(ctx)
	if err != nil {
		return err
	}
	status, raw, err := s.send(ctx, method, path, q, body, token, clientID)
	var env controlEnvelope
	envErr := json.Unmarshal(raw, &env)
	if err == nil && (status == http.StatusUnauthorized || status == http.StatusForbidden || authCodes[env.ErrorCode]) {
		fresh, ferr := s.c.afterRejection(ctx, token)
		if ferr != nil {
			return ferr
		}
		token = fresh
		status, raw, err = s.send(ctx, method, path, q, body, token, clientID)
		env = controlEnvelope{}
		envErr = json.Unmarshal(raw, &env)
	}
	if err != nil {
		return fmt.Errorf("dws: events %s: %w", path, err)
	}
	fail := func(kind ErrorKind, code, msg string) error {
		msg = strings.ReplaceAll(msg, token, "[redacted]")
		return &Error{Server: "events", Tool: strings.TrimPrefix(path, "/"), Kind: kind, Status: status, Code: clip(code, 64), Message: clip(msg, 300)}
	}
	switch {
	case env.ErrorCode != "" || (envErr == nil && env.Success != nil && !*env.Success):
		return fail(KindBusiness, env.ErrorCode, env.ErrorMsg)
	case status < 200 || status > 299:
		return fail(KindHTTP, "", env.ErrorMsg)
	case envErr != nil:
		return fail(KindDecode, "", "unreadable response body")
	}
	if out != nil {
		if env.Success != nil {
			*out = env.Result
		} else {
			*out = raw
		}
	}
	return nil
}

func (s *EventService) send(ctx context.Context, method, path string, q url.Values, body any, token, clientID string) (int, []byte, error) {
	target := s.c.cfg.authURL() + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-user-access-token", token)
	req.Header.Set("X-DWS-Client-Id", clientID)
	req.Header.Set("X-DWS-Source-Id", DefaultSourceID)
	corp := s.c.identity.CorpID
	if corp == "" {
		corp = s.c.Token().CorpID
	}
	if corp != "" {
		req.Header.Set("X-DWS-Corp-Id", corp)
	}
	resp, err := s.c.cfg.httpClient().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, err
}
