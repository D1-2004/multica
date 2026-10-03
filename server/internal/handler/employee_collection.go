package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/taskinput"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Cross-scene collection wiring (docs/plans/2026-10-03/employee-loop-backend-delivery/02-collection.md, A2).
//
// The origin scene's Loop creates a collection with create_collection: one
// v2 explicit-goal Task, its collection wait, the invitations and one outbox
// send per invitation commit in the tool journal transaction. A participant's
// message in its own scene is bound by the Host (taskinput.BindAnswer) before
// the model runs; the model only decides whether that message is the answer
// and records it with accept_collection_input. The collection reconciler turns
// a ready intent into one collection.ready Task wake for the origin scene,
// whose completion sends one summary and completes the collection and goal in
// the same transaction.

const (
	employeeCollectionMaxParticipants = 20
	employeeCollectionMaxGoalBytes    = 4000
	employeeCollectionSourceNamespace = "employee_scene.collection"
	employeeCollectionAnswerNamespace = "employee_scene.message"
	employeeCollectionLookbackDays    = 90
)

// CollectionReminderPolicy is a requester-authorized reminder plan frozen with
// a collection: the requester's exact words, at most MaxCount reminders.
type CollectionReminderPolicy struct {
	CollectionID     string
	RequesterRef     string
	InstructionQuote string
	MaxCount         int
	Interval         time.Duration
	FirstDelay       time.Duration
}

// CollectionReminderRecorder persists a reminder plan in the collection's
// creation transaction. The reminder package owns it; nil means reminders are
// unavailable and a request for them is refused, never silently dropped.
type CollectionReminderRecorder func(ctx context.Context, tx pgx.Tx, scope taskinput.Scope, policy CollectionReminderPolicy) error

func employeeCollectionTools(source map[string]any, stringField func(string) map[string]any) []employeeloop.Tool {
	participant := map[string]any{"type": "object", "properties": map[string]any{
		"name":    stringField("The person's display name exactly as the requester gave it."),
		"channel": map[string]any{"type": "string", "enum": []string{"dm", "group"}, "description": "dm: ask them privately. group: ask them in a group conversation where they have talked with you."},
		"group":   stringField("For channel=group: the group's exact title, if the requester named one."),
	}, "required": []string{"name", "channel"}, "additionalProperties": false}
	reminders := map[string]any{"type": "object", "description": "Only when the requester explicitly asked to remind people who have not answered. Quote their exact words.", "properties": map[string]any{
		"instruction_quote":   stringField("Exact wording in the selected source that asks for reminders."),
		"max_count":           map[string]any{"type": "integer", "minimum": 1, "maximum": 3},
		"interval_minutes":    map[string]any{"type": "integer", "minimum": 30, "maximum": 10080},
		"first_delay_minutes": map[string]any{"type": "integer", "minimum": 30, "maximum": 10080},
	}, "required": []string{"instruction_quote"}, "additionalProperties": false}
	deadline := map[string]any{"type": "object", "description": "Only when the requester stated an explicit deadline.", "properties": map[string]any{
		"at":        stringField("RFC 3339 time with offset, e.g. 2026-10-09T18:00:00+08:00."),
		"time_zone": stringField("IANA time zone, e.g. Asia/Shanghai."),
	}, "required": []string{"at", "time_zone"}, "additionalProperties": false}
	return []employeeloop.Tool{
		{Name: "create_collection", Effect: true, Description: "Ask named people, in their own conversations with you, one bounded question on behalf of the requester of the selected source, and summarize their answers back here once everyone has answered. This is the only way to ask people something and get their replies back (e.g. 帮我私聊问一下某人…回复后汇总给我): a background task cannot receive replies, so never use dispatch_task for it. Use it whenever this source asks you to ask or collect answers or information from specific people; each such request is a new collection, even for a person already asked something else. Write the question for them: no private details from this conversation, memory or other people's answers. People are resolved only from conversations they have had with you; if the Host reports a name as unknown or ambiguous, ask the requester. Include the acknowledgement to send after the invitations are committed; it does not mean anyone has received or answered yet.", Schema: map[string]any{"type": "object", "properties": map[string]any{
			"source_ref":   source,
			"goal":         stringField("What the requester wants collected and why, in their words."),
			"question":     stringField("The exact question each person receives. Self-contained, polite, at most a few sentences."),
			"participants": map[string]any{"type": "array", "minItems": 1, "maxItems": employeeCollectionMaxParticipants, "items": participant},
			"deadline":     deadline,
			"reminders":    reminders,
			"reply":        stringField("Brief natural acknowledgement to the requester, e.g. 我去问一下，收齐后汇总给你. Do not claim anyone has answered."),
		}, "required": []string{"source_ref", "goal", "question", "participants", "reply"}, "additionalProperties": false}},
		{Name: "read_collection", Description: "Read collections this requester started from this conversation (who answered, their answers, who is still pending), and the sender's own open invitations in this conversation. Use for progress questions about collected answers.", Schema: map[string]any{"type": "object", "properties": map[string]any{"source_ref": source}, "required": []string{"source_ref"}, "additionalProperties": false}},
		{Name: "accept_collection_input", Effect: true, Terminal: employeeloop.Reply, Description: "Record the selected source message as the sender's answer to one of their own invitations listed in the invitation context, then send a short acknowledgement. Use only when the message actually answers that question; a question back, 'later', thanks or chat is not an answer. When the Host marks the binding ambiguous, ask which question they are answering instead, unless this message itself clearly names it (then quote those words). A replacement of an earlier answer needs correction=true.", Schema: map[string]any{"type": "object", "properties": map[string]any{
			"source_ref":      source,
			"invitation_ref":  stringField("invitation_ref from the invitation context of this source."),
			"correction":      map[string]any{"type": "boolean", "description": "True only when the sender explicitly replaces an earlier answer to the same question."},
			"reference_quote": stringField("Only when the Host binding is ambiguous or missing: exact words of this message that name which question it answers."),
			"reply":           stringField("Short natural acknowledgement to the sender. Do not mention other people, totals or the requester's other context."),
		}, "required": []string{"source_ref", "invitation_ref", "reply"}, "additionalProperties": false}},
	}
}

func isEmployeeCollectionTool(name string) bool {
	return name == "create_collection" || name == "read_collection" || name == "accept_collection_input"
}

func employeeTaskinputScope(scope employeeentry.Scope) taskinput.Scope {
	return taskinput.Scope{WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, TenantOrgID: scope.TenantOrgID}
}

// employeeSourceAddressed mirrors dispatch_task's group gate: a group source
// must address this employee unless the conversation is proactive.
func employeeSourceAddressed(env employeeDispatchEnvelope, source employeeSourceMessage) bool {
	if !strings.EqualFold(env.Command.Event.Data.Conversation.Type, "group") || env.Command.ProactiveConversation || source.Message.Mentions == nil {
		return true
	}
	for _, mention := range source.Message.Mentions {
		if env.Command.ExternalIdentity.DWS != nil && mention.UID == env.Command.ExternalIdentity.DWS.UID {
			return true
		}
	}
	return false
}

var employeePlaceholderText = regexp.MustCompile(`^\[[^\[\]\n]{1,32}\]$`)

// employeeMessageKind classifies the provider message. A bracketed localized
// placeholder ("[互动卡片]", "[图片]") is a card or media summary the Host
// cannot read as an answer; reactions and empty messages are notices.
func employeeMessageKind(m DispatchMessage) taskinput.MessageKind {
	if m.Reaction != nil {
		return taskinput.MessageSystem
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return taskinput.MessageSystem
	}
	if employeePlaceholderText.MatchString(text) {
		return taskinput.MessageCard
	}
	return taskinput.MessageText
}

// employeeSenderKind: this employee's own identity never answers. Native and
// Router events carry no robot flag, so other senders count as accounts of
// people (a digital employee's own account answers like a person).
func employeeSenderKind(env employeeDispatchEnvelope, m DispatchMessage) taskinput.SenderKind {
	if dws := env.Command.ExternalIdentity.DWS; dws != nil && dws.UID != "" && strings.TrimSpace(m.SenderUID) == dws.UID {
		return taskinput.SenderSelf
	}
	return taskinput.SenderPerson
}

func employeeMessageOccurredAt(m DispatchMessage, fallback time.Time) time.Time {
	if m.OccurredAt > 0 {
		return time.UnixMilli(m.OccurredAt).UTC()
	}
	return fallback.UTC()
}

// ---- Participant resolution ----

type employeeCollectionPerson struct {
	SceneID    string
	SceneKind  string
	SceneTitle string
	ExternalID string
	Ref        string
	Name       string
	OpenID     string
}

// employeeCollectionPeople lists where a display name has spoken to this
// employee in this tenant: the provider-attested sender identities of admitted
// messages. Nothing is guessed from text or minted from a person.
func employeeCollectionPeople(ctx context.Context, tx pgx.Tx, scope employeeentry.Scope, name string, partial bool) ([]employeeCollectionPerson, error) {
	match := `lower(btrim(m.value->>'senderDisplayName'))=lower($4)`
	if partial {
		match = `strpos(lower(btrim(m.value->>'senderDisplayName')), lower($4))>0`
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (c.scene_id, ref) c.scene_id::text, s.scene_kind, s.title, s.external_scene_id, ref, name, open_id FROM (
 SELECT c.scene_id, c.created_at,
  'dingtalk:'||c.tenant_org_id||':'||CASE WHEN btrim(COALESCE(m.value->>'senderUid',''))<>'' THEN 'uid:'||btrim(m.value->>'senderUid') WHEN btrim(COALESCE(m.value->>'senderOpenDingTalkId',''))<>'' THEN 'open_id:'||btrim(m.value->>'senderOpenDingTalkId') WHEN btrim(COALESCE(m.value->>'senderStaffId',''))<>'' THEN 'staff_id:'||btrim(m.value->>'senderStaffId') ELSE '' END AS ref,
  btrim(m.value->>'senderDisplayName') AS name, btrim(COALESCE(m.value->>'senderOpenDingTalkId','')) AS open_id
 FROM employee_event_consumption c
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(c.payload#>'{command,event,data,messages}')='array' THEN c.payload#>'{command,event,data,messages}' ELSE '[]'::jsonb END) m(value)
 WHERE c.workspace_id=$1::uuid AND c.agent_id=$2::uuid AND c.tenant_org_id=$3 AND c.owner_loop='employee' AND c.reason=''
 AND c.created_at > now() - make_interval(days => $5) AND `+match+`
) c JOIN agent_scene s ON s.id=c.scene_id AND s.workspace_id=$1::uuid AND s.agent_id=$2::uuid AND s.tenant_org_id=$3
WHERE c.ref NOT LIKE '%:' ORDER BY c.scene_id, ref, c.created_at DESC LIMIT 50`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, name, employeeCollectionLookbackDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	people := []employeeCollectionPerson{}
	for rows.Next() {
		var p employeeCollectionPerson
		if err := rows.Scan(&p.SceneID, &p.SceneKind, &p.SceneTitle, &p.ExternalID, &p.Ref, &p.Name, &p.OpenID); err != nil {
			return nil, err
		}
		people = append(people, p)
	}
	return people, rows.Err()
}

type employeeCollectionTarget struct {
	spec      taskinput.InvitationSpec
	openID    string
	sceneKind string
	external  string
	channel   string
}

type employeeCollectionResolveError struct{ msg string }

func (e *employeeCollectionResolveError) Error() string { return e.msg }

func employeeResolveCollectionTarget(ctx context.Context, tx pgx.Tx, scope employeeentry.Scope, name, channel, group string) (employeeCollectionTarget, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return employeeCollectionTarget{}, &employeeCollectionResolveError{"participant name is required"}
	}
	people, err := employeeCollectionPeople(ctx, tx, scope, name, false)
	if err == nil && len(people) == 0 && utf8.RuneCountInString(name) >= 2 {
		// A name the requester shortened ("Director") still identifies one
		// person when exactly one known sender's display name contains it.
		people, err = employeeCollectionPeople(ctx, tx, scope, name, true)
	}
	if err != nil {
		return employeeCollectionTarget{}, err
	}
	refs := map[string]bool{}
	for _, p := range people {
		refs[p.Ref] = true
	}
	switch len(refs) {
	case 0:
		return employeeCollectionTarget{}, &employeeCollectionResolveError{fmt.Sprintf("%s has not talked with me in this organization, so I cannot reach them; ask the requester to have them message me first", name)}
	case 1:
	default:
		return employeeCollectionTarget{}, &employeeCollectionResolveError{fmt.Sprintf("more than one person named %s has talked with me; ask the requester which one", name)}
	}
	label := people[0].Name
	ref := people[0].Ref
	target := employeeCollectionTarget{channel: channel, spec: taskinput.InvitationSpec{ParticipantRef: ref, ParticipantLabel: employeeClipLabel(label)}}
	switch channel {
	case "dm":
		for _, p := range people {
			if p.SceneKind == scene.KindDM {
				target.spec.TargetSceneID, target.sceneKind, target.external, target.openID = p.SceneID, scene.KindDM, p.ExternalID, p.OpenID
			}
		}
		if target.spec.TargetSceneID == "" {
			// They spoke to me only in a group: send a 1:1 message by their
			// viewer-relative open id; the DM scene comes from the receipt.
			for _, p := range people {
				if p.OpenID != "" {
					target.openID = p.OpenID
				}
			}
			if target.openID == "" {
				return employeeCollectionTarget{}, &employeeCollectionResolveError{fmt.Sprintf("I have no private address for %s", name)}
			}
			target.spec.TargetKind, target.sceneKind = scene.KindDM, scene.KindDM
		}
		if target.openID == "" {
			return employeeCollectionTarget{}, &employeeCollectionResolveError{fmt.Sprintf("I have no private address for %s", name)}
		}
	case "group":
		var matches []employeeCollectionPerson
		for _, p := range people {
			if p.SceneKind == scene.KindGroup && (strings.TrimSpace(group) == "" || strings.EqualFold(strings.TrimSpace(p.SceneTitle), strings.TrimSpace(group))) {
				matches = append(matches, p)
			}
		}
		switch len(matches) {
		case 0:
			return employeeCollectionTarget{}, &employeeCollectionResolveError{fmt.Sprintf("%s has not talked with me in that group; ask the requester for another group or a private message", name)}
		case 1:
		default:
			titles := []string{}
			for _, m := range matches {
				titles = append(titles, m.SceneTitle)
			}
			return employeeCollectionTarget{}, &employeeCollectionResolveError{fmt.Sprintf("%s talks with me in several groups (%s); ask the requester which group", name, strings.Join(titles, ", "))}
		}
		target.spec.TargetSceneID, target.sceneKind, target.external, target.openID = matches[0].SceneID, scene.KindGroup, matches[0].ExternalID, matches[0].OpenID
	default:
		return employeeCollectionTarget{}, &employeeCollectionResolveError{"channel must be dm or group"}
	}
	return target, nil
}

func employeeClipLabel(s string) string {
	if utf8.RuneCountInString(s) <= 128 {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:128]))
}

// ---- Invitation rendering and egress ----

// employeeInvitationText is the Host template around the requester-authorized
// question. Only the question is model-written.
func employeeInvitationText(requester, question, sceneKind string) string {
	requester = strings.TrimSpace(requester)
	if requester == "" {
		requester = "同事"
	}
	if sceneKind == scene.KindGroup {
		return requester + " 请我向你收集一个信息：\n" + question + "\n请引用回复这条消息作答。"
	}
	return requester + " 请我向你收集一个信息：\n" + question + "\n直接回复即可。"
}

// employeeCollectionPrivateFragments are origin-private texts the question must
// not copy: the frozen recent conversation (other than the request itself)
// and private memory lines of this snapshot.
func employeeCollectionPrivateFragments(saved employeeSavedInput, sourceText string) []taskinput.PrivateFragment {
	out := []taskinput.PrivateFragment{}
	var history employeeentry.RecentConversation
	if json.Unmarshal([]byte(saved.Input.RecentConversation), &history) == nil {
		for _, m := range history.Messages {
			if text := strings.TrimSpace(m.Text); text != "" && text != strings.TrimSpace(sourceText) {
				out = append(out, taskinput.PrivateFragment{Text: text})
			}
		}
	}
	for _, line := range strings.Split(saved.Input.Memory, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "Requester-private") && !strings.HasPrefix(line, "Scene memory") {
			out = append(out, taskinput.PrivateFragment{Text: line})
		}
	}
	return out
}

func (h *employeeSceneHost) savedInput(ctx context.Context, tx pgx.Tx) (employeeSavedInput, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT input_snapshot FROM employee_scene_job WHERE id=$1::uuid`, h.job.ID).Scan(&raw); err != nil {
		return employeeSavedInput{}, err
	}
	var saved employeeSavedInput
	return saved, json.Unmarshal(raw, &saved)
}

// employeeHistoryPrincipal is the principal whose recent dialogue in another
// scene of this agent shows a Host message: the agent's dispatch endpoint
// actor, under which that scene's messages are admitted.
func employeeHistoryPrincipal(ctx context.Context, q *db.Queries, scope employeeentry.Scope) (string, error) {
	ep, err := q.GetAgentDispatchEndpoint(ctx, parseUUID(scope.AgentID))
	if err != nil {
		return "", err
	}
	if uuidToString(ep.WorkspaceID) != scope.WorkspaceID {
		return "", errors.New("agent dispatch endpoint is outside this workspace")
	}
	return uuidToString(ep.ActorUserID), nil
}

// ---- create_collection ----

type employeeCollectionCreated struct {
	CollectionID string                            `json:"collection_id"`
	TaskID       string                            `json:"task_id"`
	Invitations  []employeeCollectionCreatedInvite `json:"invitations"`
	Reminders    bool                              `json:"reminders"`
}
type employeeCollectionCreatedInvite struct {
	Participant string `json:"participant"`
	Channel     string `json:"channel"`
	State       string `json:"state"`
}

func (h *employeeSceneHost) createCollection(ctx context.Context, tx pgx.Tx, source employeeSourceMessage, env employeeDispatchEnvelope, call employeeloop.ToolCall) (employeeloop.ToolResult, error) {
	if source.Message.Reaction != nil || env.Command.Continuation != nil {
		return employeeloop.ToolResult{}, errors.New("a reaction or continuation cannot start a collection")
	}
	if !employeeSourceAddressed(env, source) {
		return employeeloop.ToolResult{}, errors.New("source message does not address this employee")
	}
	if env.Command.ExternalIdentity.DWS == nil || env.Command.ExternalIdentity.DWS.UID == "" {
		return employeeloop.ToolResult{}, errors.New("this conversation has no employee identity to send from")
	}
	goal, err := argument(call.Arguments, "goal")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	question, err := argument(call.Arguments, "question")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	reply, err := argument(call.Arguments, "reply")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	question = strings.TrimSpace(question)
	if len(goal) > employeeCollectionMaxGoalBytes || utf8.RuneCountInString(question) > taskinput.MaxQuestionRunes || len(reply) > 8000 {
		return employeeloop.ToolResult{}, errors.New("collection goal, question or reply exceeds bounds")
	}
	rawParticipants, ok := call.Arguments["participants"].([]any)
	if !ok || len(rawParticipants) == 0 || len(rawParticipants) > employeeCollectionMaxParticipants {
		return employeeloop.ToolResult{}, fmt.Errorf("participants must list 1-%d people", employeeCollectionMaxParticipants)
	}
	var deadline *taskinput.Deadline
	if raw, present := call.Arguments["deadline"]; present {
		object, ok := raw.(map[string]any)
		if !ok {
			return employeeloop.ToolResult{}, errors.New("deadline must be an object")
		}
		at, _ := object["at"].(string)
		zone, _ := object["time_zone"].(string)
		parsed, perr := time.Parse(time.RFC3339, strings.TrimSpace(at))
		if perr != nil {
			return employeeloop.ToolResult{}, errors.New("deadline.at must be RFC 3339 with an offset")
		}
		deadline = &taskinput.Deadline{At: parsed.UTC(), TimeZone: strings.TrimSpace(zone)}
	}
	var reminder *CollectionReminderPolicy
	if raw, present := call.Arguments["reminders"]; present {
		object, ok := raw.(map[string]any)
		if !ok {
			return employeeloop.ToolResult{}, errors.New("reminders must be an object")
		}
		quote, _ := object["instruction_quote"].(string)
		quote = strings.TrimSpace(quote)
		if quote == "" || !strings.Contains(source.Message.Text, quote) {
			return employeeloop.ToolResult{}, errors.New("reminders need the requester's exact words from this source")
		}
		number := func(key string, fallback int) int {
			if v, ok := object[key].(float64); ok && v == float64(int(v)) {
				return int(v)
			}
			return fallback
		}
		reminder = &CollectionReminderPolicy{RequesterRef: source.RequesterRef, InstructionQuote: quote, MaxCount: number("max_count", 1), Interval: time.Duration(number("interval_minutes", 24*60)) * time.Minute, FirstDelay: time.Duration(number("first_delay_minutes", 24*60)) * time.Minute}
		if h.worker.CollectionReminders == nil {
			return employeeloop.ToolResult{}, errors.New("reminders are not available yet; create the collection without reminders and tell the requester")
		}
	}

	saved, err := h.savedInput(ctx, tx)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	scopeIn := employeeTaskinputScope(h.job.Scope)
	targets := make([]employeeCollectionTarget, 0, len(rawParticipants))
	seen := map[string]bool{}
	for _, raw := range rawParticipants {
		object, ok := raw.(map[string]any)
		if !ok {
			return employeeloop.ToolResult{}, errors.New("each participant must be an object")
		}
		name, _ := object["name"].(string)
		channel, _ := object["channel"].(string)
		group, _ := object["group"].(string)
		target, err := employeeResolveCollectionTarget(ctx, tx, h.job.Scope, name, channel, group)
		if err != nil {
			return employeeloop.ToolResult{}, err
		}
		if target.spec.ParticipantRef == source.RequesterRef {
			return employeeloop.ToolResult{}, errors.New("the requester cannot be a participant of their own collection")
		}
		if seen[target.spec.ParticipantRef] {
			return employeeloop.ToolResult{}, fmt.Errorf("%s is listed twice", name)
		}
		seen[target.spec.ParticipantRef] = true
		target.spec.Question = question
		targets = append(targets, target)
	}
	// The final bytes of every invitation pass the egress check before
	// anything is created, so a held question can be rephrased.
	private := employeeCollectionPrivateFragments(saved, source.Message.Text)
	rendered := make([]string, len(targets))
	for i, target := range targets {
		rendered[i] = employeeInvitationText(source.Message.SenderDisplayName, question, target.sceneKind)
		audience, _ := taskinput.AudienceForSceneKind(target.sceneKind)
		if check := taskinput.CheckEgress(taskinput.EgressInput{Rendered: rendered[i], Audience: audience, TargetSceneKind: target.sceneKind, Private: private}); check.Held {
			return employeeloop.ToolResult{}, fmt.Errorf("the question for %s was held (%s): rewrite it without private conversation details, secrets or links", target.spec.ParticipantLabel, strings.Join(check.Reasons, ","))
		}
	}

	evidence, err := json.Marshal(source)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	sourceKey := source.ReceiptID + "/" + call.NativeToolCallID
	taskScope := h.taskScope()
	task, err := employeetask.NewStore(tx).Create(ctx, employeetask.CreateParams{Scope: taskScope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect,
		RequesterRef: source.RequesterRef, Definition: employeetask.Definition{Goal: goal, Deliverables: []string{"one summary of the collected answers in this conversation"}},
		Source: employeetask.Source{Namespace: employeeentry.TaskOriginNamespace, Key: sourceKey + "/definition"}, Input: string(evidence),
		Lifecycle: employeetask.LifecycleV2, CompletionMode: employeetask.CompletionExplicitGoal})
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	specs := make([]taskinput.InvitationSpec, len(targets))
	for i, target := range targets {
		specs[i] = target.spec
	}
	authority := taskinput.Authority{ActorRef: source.RequesterRef, SceneID: h.job.Scope.SceneID, ReceiptRef: source.ReceiptID, VerifiedAt: time.Now()}
	collections := taskinput.NewStore(tx)
	col, invitations, err := collections.CreateCollectionTx(ctx, scopeIn, taskinput.CreateCollectionParams{TaskID: task.ID, OriginSceneID: h.job.Scope.SceneID,
		AuthorityRef: employeeentry.TaskOriginNamespace + ":" + sourceKey, RequesterRef: source.RequesterRef, DeliveryAnchorRef: source.SourceRef, GoalRevision: task.GoalRevision,
		Deadline: deadline, Invitations: specs, Source: taskinput.Source{Namespace: employeeCollectionSourceNamespace, Key: sourceKey}, Authority: authority})
	if err != nil {
		return employeeloop.ToolResult{}, employeeCollectionError(err)
	}
	if _, _, err = employeetask.WaitTaskTx(ctx, tx, taskScope, task.ID, employeetask.WaitParams{Source: employeetask.Source{Namespace: employeeCollectionSourceNamespace, Key: sourceKey + "/wait"},
		Kind: employeetask.WaitCollection, RefID: col.ID, Mandatory: true, AuthorityRef: col.AuthorityRef}); err != nil {
		return employeeloop.ToolResult{}, err
	}
	if reminder != nil {
		reminder.CollectionID = col.ID
		if err = h.worker.CollectionReminders(ctx, tx, scopeIn, *reminder); err != nil {
			return employeeloop.ToolResult{}, fmt.Errorf("reminders: %w", err)
		}
	}
	if h.worker.handler.DingTalkResponses == nil {
		return employeeloop.ToolResult{}, errors.New("employee response outbox is unavailable")
	}
	q := db.New(tx)
	principal, err := employeeHistoryPrincipal(ctx, q, h.job.Scope)
	if err != nil {
		return employeeloop.ToolResult{}, fmt.Errorf("history principal: %w", err)
	}
	created := employeeCollectionCreated{CollectionID: col.ID, TaskID: task.ID, Reminders: reminder != nil}
	for i, inv := range invitations {
		target := targets[i]
		in := dingtalkresponse.ActionInput{WorkspaceID: h.job.Scope.WorkspaceID, AgentID: h.job.Scope.AgentID, DWSUID: env.Command.ExternalIdentity.DWS.UID, DWSOrgID: h.job.Scope.TenantOrgID,
			SceneID: inv.TargetSceneID, ConversationID: target.external, IsGroup: target.sceneKind == scene.KindGroup, SenderOpenDingTalkID: target.openID,
			DWSEnvironment: commandDWSEnvironment(env.Command), Text: rendered[i]}
		if env.Command.ResponsePolicy != nil {
			in.ShowAITag = env.Command.ResponsePolicy.ShowAITag
		}
		actionID, err := h.worker.handler.DingTalkResponses.EnqueueInvitationNotice(ctx, tx, in, inv.DeliveryActionID)
		if err != nil {
			return employeeloop.ToolResult{}, fmt.Errorf("invitation send: %w", err)
		}
		if inv.TargetSceneID != "" {
			// A pending-scene invitation records its history fact once the
			// receipt names its conversation (collection reconciler).
			if err = employeeentry.RecordHostNotice(ctx, tx, employeeentry.HostNotice{ActionID: actionID, Scope: employeeentry.Scope{WorkspaceID: h.job.Scope.WorkspaceID, AgentID: h.job.Scope.AgentID, TenantOrgID: h.job.Scope.TenantOrgID, SceneID: inv.TargetSceneID},
				PrincipalID: principal, SourceKind: employeeentry.HostNoticeInvitation, SourceID: inv.ID}); err != nil {
				return employeeloop.ToolResult{}, fmt.Errorf("invitation history: %w", err)
			}
		}
		created.Invitations = append(created.Invitations, employeeCollectionCreatedInvite{Participant: inv.ParticipantLabel, Channel: target.channel, State: "sending"})
	}
	raw, err := json.Marshal(created)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	return employeeloop.ToolResult{Content: string(raw), Receipt: col.ID, Terminal: &employeeloop.Decision{Kind: employeeloop.Dispatched, Reply: strings.TrimSpace(reply)}}, nil
}

// employeeCollectionError turns domain refusals into model-readable reasons.
func employeeCollectionError(err error) error {
	switch {
	case errors.Is(err, taskinput.ErrClosed):
		return fmt.Errorf("the collection or invitation is closed: %w", err)
	case errors.Is(err, taskinput.ErrAlreadyAnswered):
		return errors.New("this invitation already has an answer; record this message only if the sender explicitly replaces it (correction=true)")
	case errors.Is(err, taskinput.ErrDeliveryPending):
		return errors.New("the invitation has not been confirmed as delivered yet")
	case errors.Is(err, taskinput.ErrNotFound):
		return errors.New("no such invitation for this sender in this conversation")
	case errors.Is(err, taskinput.ErrForbidden):
		return errors.New("only the original requester can do that")
	}
	return err
}

// ---- read_collection ----

type employeeCollectionReadView struct {
	Collections []employeeCollectionSummaryView `json:"collections"`
	OwnInvites  []employeeOwnInvitationView     `json:"your_open_invitations,omitempty"`
}
type employeeCollectionSummaryView struct {
	Goal      string                       `json:"goal,omitempty"`
	State     string                       `json:"state"`
	Expected  int                          `json:"expected"`
	Received  int                          `json:"received"`
	Deadline  *taskinput.Deadline          `json:"deadline,omitempty"`
	ClosedBy  string                       `json:"closed_early,omitempty"`
	Responses []employeeCollectionSlotView `json:"responses"`
}
type employeeCollectionSlotView struct {
	Participant string `json:"participant"`
	Question    string `json:"question"`
	State       string `json:"state"`
	Answer      string `json:"answer,omitempty"`
	Corrected   bool   `json:"corrected,omitempty"`
}
type employeeOwnInvitationView struct {
	Question string `json:"question"`
	State    string `json:"state"`
	Answer   string `json:"your_answer,omitempty"`
}

func employeeCollectionSlots(view taskinput.OriginView) []employeeCollectionSlotView {
	out := make([]employeeCollectionSlotView, 0, len(view.Slots))
	for _, slot := range view.Slots {
		s := employeeCollectionSlotView{Participant: slot.ParticipantLabel, Question: employeeTaskData(slot.Question, 2000), State: string(slot.State)}
		if s.Participant == "" {
			s.Participant = fmt.Sprintf("participant %d", slot.Ordinal)
		}
		if slot.Answer != nil {
			s.Answer, s.Corrected = employeeTaskData(slot.Answer.Body, 2048), slot.Answer.Corrected
			if s.Answer == "" && slot.Answer.BodyRef != "" {
				s.Answer = "(attachment)"
			}
		}
		out = append(out, s)
	}
	return out
}

func (h *employeeSceneHost) readCollection(ctx context.Context, tx pgx.Tx, source employeeSourceMessage) (employeeloop.ToolResult, error) {
	scopeIn := employeeTaskinputScope(h.job.Scope)
	rows, err := tx.Query(ctx, `SELECT c.id::text, c.task_id::text, t.definition->>'goal' FROM employee_task_collection c
 JOIN employee_task t ON t.id=c.task_id AND t.workspace_id=c.workspace_id AND t.agent_id=c.agent_id AND t.tenant_org_id=c.tenant_org_id
 WHERE c.workspace_id=$1::uuid AND c.agent_id=$2::uuid AND c.tenant_org_id=$3 AND c.origin_scene_id=$4::uuid AND t.requester_ref=$5 AND c.requester_ref=$5
 AND (c.state IN ('open','ready','summarizing') OR c.updated_at > now()-interval '7 days')
 ORDER BY c.created_at DESC LIMIT 5`, h.job.Scope.WorkspaceID, h.job.Scope.AgentID, h.job.Scope.TenantOrgID, h.job.Scope.SceneID, source.RequesterRef)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	type ref struct{ id, task, goal string }
	refs := []ref{}
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.task, &r.goal); err != nil {
			rows.Close()
			return employeeloop.ToolResult{}, err
		}
		refs = append(refs, r)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return employeeloop.ToolResult{}, err
	}
	store := taskinput.NewStore(tx)
	out := employeeCollectionReadView{Collections: []employeeCollectionSummaryView{}}
	for _, r := range refs {
		view, err := store.ReadOriginInputs(ctx, scopeIn, r.id, taskinput.OriginViewer{TaskID: r.task, SceneID: h.job.Scope.SceneID})
		if err != nil {
			return employeeloop.ToolResult{}, err
		}
		summary := employeeCollectionSummaryView{Goal: employeeTaskData(r.goal, 1000), State: string(view.State), Expected: view.Expected, Received: view.Received, Deadline: view.Deadline, Responses: employeeCollectionSlots(view)}
		if view.CloseMode == string(taskinput.ClosePartial) {
			summary.ClosedBy = "requester"
		}
		out.Collections = append(out.Collections, summary)
	}
	own, err := store.ListParticipantInvitations(ctx, scopeIn, taskinput.ParticipantViewer{ActorRef: source.RequesterRef, SceneID: h.job.Scope.SceneID})
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	for _, inv := range own {
		v := employeeOwnInvitationView{Question: employeeTaskData(inv.Question, 2000), State: string(inv.State)}
		if inv.Answer != nil {
			v.Answer = employeeTaskData(inv.Answer.Body, 2048)
		}
		out.OwnInvites = append(out.OwnInvites, v)
	}
	raw, err := json.Marshal(out)
	return employeeloop.ToolResult{Content: string(raw)}, err
}

// ---- B-scene binding (frozen before the model) ----

// employeeInvitationBinding is the Host's binding of one current source
// message to the sender's own invitations, frozen with the chat snapshot.
type employeeInvitationBinding struct {
	SourceRef  string                        `json:"source_ref"`
	Outcome    string                        `json:"outcome"`
	Kind       string                        `json:"kind,omitempty"`
	Reason     string                        `json:"reason,omitempty"`
	Candidates []employeeInvitationCandidate `json:"candidates"`
}
type employeeInvitationCandidate struct {
	Ref          string `json:"invitation_ref"`
	InvitationID string `json:"invitation_id"`
	CollectionID string `json:"collection_id"`
	Bound        bool   `json:"bound,omitempty"`
}

// employeeReplyParents walks the scene's admitted messages back from start:
// each hop is the message a stored message quoted, at most MaxReplyHops.
func employeeReplyParents(ctx context.Context, database employeeentry.DB, scope employeeentry.Scope, start string) (map[string]string, error) {
	parents := map[string]string{}
	current := start
	for hop := 0; hop < taskinput.MaxReplyHops && current != ""; hop++ {
		var parent string
		err := database.QueryRow(ctx, `SELECT COALESCE(NULLIF(m.value#>>'{referencedMessage,openMsgId}',''), m.value#>>'{referencedMessage,messageId}','')
 FROM employee_event_consumption c
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(c.payload#>'{command,event,data,messages}')='array' THEN c.payload#>'{command,event,data,messages}' ELSE '[]'::jsonb END) m(value)
 WHERE c.workspace_id=$1::uuid AND c.agent_id=$2::uuid AND c.tenant_org_id=$3 AND c.scene_id=$4::uuid AND m.value->>'openMsgId'=$5
 AND c.created_at > now()-interval '30 days' LIMIT 1`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID, current).Scan(&parent)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		if parent == "" || parents[current] != "" {
			break
		}
		parents[current] = parent
		current = parent
	}
	return parents, nil
}

func employeeQuotedIDs(m DispatchMessage) []string {
	if m.ReferencedMessage == nil {
		return nil
	}
	ids := []string{}
	for _, id := range []string{m.ReferencedMessage.OpenMsgID, m.ReferencedMessage.MessageID} {
		if id = strings.TrimSpace(id); id != "" && (len(ids) == 0 || ids[0] != id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// invitationContext binds each current source message to its sender's own
// open invitations and renders only those questions for the model.
func (w *EmployeeSceneWorker) invitationContext(ctx context.Context, job employeeentry.Job, envelopes []employeeDispatchEnvelope) ([]employeeInvitationBinding, string, error) {
	database, ok := employeeEntryDB(w.handler)
	if !ok {
		return nil, "", errors.New("employee task storage is unavailable")
	}
	registered, err := employeeSceneFence(ctx, w.handler, job)
	if err != nil {
		return nil, "", err
	}
	store := taskinput.NewStore(database)
	scopeIn := employeeTaskinputScope(job.Scope)
	var bindings []employeeInvitationBinding
	views := []map[string]any{}
	for i, item := range job.Items {
		for _, source := range employeeSourceMessages(item, envelopes[i]) {
			if source.RequesterRef == "" || source.SourceRef == "" {
				continue
			}
			candidates, err := store.BindingCandidates(ctx, scopeIn, source.RequesterRef)
			if err != nil {
				return nil, "", err
			}
			own := candidates[:0:0]
			for _, c := range candidates {
				if c.TargetSceneID == job.Scope.SceneID {
					own = append(own, c)
				}
			}
			if len(own) == 0 {
				continue
			}
			msg := taskinput.InboundMessage{MessageID: source.Message.OpenMsgID, SceneID: job.Scope.SceneID, SceneKind: registered.SceneKind, SenderRef: source.RequesterRef,
				SenderKind: employeeSenderKind(envelopes[i], source.Message), MessageKind: employeeMessageKind(source.Message)}
			binding := taskinput.BindAnswer(msg, nil, own)
			for _, quoted := range employeeQuotedIDs(source.Message) {
				parents, err := employeeReplyParents(ctx, database, job.Scope, quoted)
				if err != nil {
					return nil, "", err
				}
				msg.ReplyToID = quoted
				binding = taskinput.BindAnswer(msg, parents, own)
				if binding.Outcome == taskinput.BindBound {
					break
				}
			}
			if binding.Outcome == taskinput.BindIgnored {
				continue
			}
			frozen := employeeInvitationBinding{SourceRef: source.SourceRef, Outcome: string(binding.Outcome), Kind: string(binding.Kind), Reason: binding.Reason}
			listed := own
			if binding.Outcome == taskinput.BindAmbiguous {
				listed = binding.Candidates
			}
			invites := []map[string]any{}
			for n, c := range listed[:min(5, len(listed))] {
				ref := fmt.Sprintf("i%d", n+1)
				bound := binding.Outcome == taskinput.BindBound && c.InvitationID == binding.InvitationID
				frozen.Candidates = append(frozen.Candidates, employeeInvitationCandidate{Ref: ref, InvitationID: c.InvitationID, CollectionID: c.CollectionID, Bound: bound})
				invites = append(invites, map[string]any{"invitation_ref": ref, "question": employeeTaskData(c.Question, 2000), "already_answered": c.State == taskinput.InvitationAnswered, "bound_to_this_message": bound})
			}
			bindings = append(bindings, frozen)
			views = append(views, map[string]any{"source_ref": source.SourceRef, "binding": frozen.Outcome, "how": frozen.Kind, "invitations": invites})
		}
	}
	if len(bindings) == 0 {
		return nil, "", nil
	}
	raw, err := json.Marshal(map[string]any{"invitation_context": views, "guidance": "You earlier asked this sender these questions on someone's behalf; only the sender's own questions are listed. " +
		"bound means the Host matched this message to that question (a reply to it, or the only open question here). If the message actually answers it, call accept_collection_input with a short thanks. " +
		"A question back, 'later', thanks or unrelated chat is not an answer. ambiguous means several of their questions fit: ask which one they are answering, unless the message itself clearly names it. " +
		"Never mention who else was asked, anyone's answers, totals, or the requester's other context."})
	return bindings, string(raw), err
}

// ---- accept_collection_input ----

func (h *employeeSceneHost) acceptCollectionInput(ctx context.Context, tx pgx.Tx, source employeeSourceMessage, env employeeDispatchEnvelope, call employeeloop.ToolCall) (employeeloop.ToolResult, error) {
	ref, err := argument(call.Arguments, "invitation_ref")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	reply, err := argument(call.Arguments, "reply")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	if len(reply) > 8000 {
		return employeeloop.ToolResult{}, errors.New("employee reply exceeds bounds")
	}
	correction, _ := call.Arguments["correction"].(bool)
	quote, _ := call.Arguments["reference_quote"].(string)
	saved, err := h.savedInput(ctx, tx)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	var binding *employeeInvitationBinding
	for i := range saved.Invitations {
		if saved.Invitations[i].SourceRef == source.SourceRef {
			binding = &saved.Invitations[i]
		}
	}
	if binding == nil {
		return employeeloop.ToolResult{}, errors.New("this message has no invitation context")
	}
	var candidate *employeeInvitationCandidate
	for i := range binding.Candidates {
		if binding.Candidates[i].Ref == ref {
			candidate = &binding.Candidates[i]
		}
	}
	if candidate == nil {
		return employeeloop.ToolResult{}, errors.New("invitation_ref is not one of this message's invitations")
	}
	registered, err := employeeSceneFence(ctx, &Handler{Queries: db.New(tx)}, h.job)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	kind := taskinput.BindingKind(binding.Kind)
	if binding.Outcome != string(taskinput.BindBound) || !candidate.Bound {
		// Only a 1:1 sender may name the question in their own words.
		quote = strings.TrimSpace(quote)
		if registered.SceneKind != scene.KindDM || utf8.RuneCountInString(quote) < 2 || !strings.Contains(source.Message.Text, quote) {
			return employeeloop.ToolResult{}, errors.New("this message is not bound to that question; ask the sender which question they are answering, or to quote the original question")
		}
		kind = taskinput.BindInviteReference
	}
	body := source.Message.Text
	if len(body) > 32<<10 {
		return employeeloop.ToolResult{}, errors.New("the answer is too long to record; ask for a shorter answer or a file")
	}
	params := taskinput.AcceptInputParams{CollectionID: candidate.CollectionID, InvitationID: candidate.InvitationID,
		Source:     taskinput.Source{Namespace: employeeCollectionAnswerNamespace, Key: source.SourceRef},
		Authority:  taskinput.Authority{ActorRef: source.RequesterRef, SceneID: h.job.Scope.SceneID, ReceiptRef: source.ReceiptID, VerifiedAt: time.Now()},
		SenderKind: employeeSenderKind(env, source.Message), MessageKind: employeeMessageKind(source.Message), Binding: kind,
		ProviderMessageID: source.Message.OpenMsgID, OccurredAt: employeeMessageOccurredAt(source.Message, h.job.CreatedAt), Body: body, Correction: correction}
	store := taskinput.NewStore(tx)
	scopeIn := employeeTaskinputScope(h.job.Scope)
	var result taskinput.AcceptResult
	for attempt := 0; ; attempt++ {
		current, err := store.GetCollection(ctx, scopeIn, candidate.CollectionID)
		if err != nil {
			return employeeloop.ToolResult{}, employeeCollectionError(err)
		}
		params.ExpectedRevision = current.Revision
		result, err = store.AcceptInputTx(ctx, scopeIn, params)
		if errors.Is(err, taskinput.ErrStaleRevision) && attempt < 4 {
			continue
		}
		if err != nil {
			return employeeloop.ToolResult{}, employeeCollectionError(err)
		}
		break
	}
	// The participant learns only that their own answer was recorded.
	raw, _ := json.Marshal(map[string]any{"recorded": true, "correction": result.Input.Kind == "correction"})
	return employeeloop.ToolResult{Content: string(raw), Receipt: result.Input.ID, Terminal: &employeeloop.Decision{Kind: employeeloop.Reply, Reply: strings.TrimSpace(reply)}}, nil
}
