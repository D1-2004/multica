package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeeresource"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// employeeResourceProvider performs provider reads as the agent's bound DWS
// identity. *dingtalkresponse.Service implements it.
type employeeResourceProvider interface {
	ReadMessageResources(ctx context.Context, in dingtalkresponse.ActionInput, conversationID, messageID string) (dwsclient.MessageResources, error)
	DownloadMessageFile(ctx context.Context, in dingtalkresponse.ActionInput, fileID string, maxBytes int64) (dwsclient.MessageFile, error)
}

const (
	// employeeResourceReadTimeout keeps room for model requests inside the
	// 45-second foreground wake.
	employeeResourceReadTimeout = 8 * time.Second
	// employeeResourceMaxMessageReads bounds provider message reads per wake,
	// current and quoted messages together.
	employeeResourceMaxMessageReads = 8

	employeeResourceReasonMessageUnverified = "message_unverified"
	employeeResourceReasonReadLimit         = "read_limit"
)

var errEmployeeResourceScope = errors.New("employee resource source is outside the claimed job scope")

// employeeMessageResourceReader builds the bounded ResourceContext of a
// claimed job's source messages. Resources come only from the provider's
// exact record of the current message, or of the message it exactly quotes;
// message text, links, Router attachment URLs and nested quote lists never
// grant access. Provider I/O runs outside database transactions, and the
// first authorized read of each resource is frozen under the job lease after
// rechecking authority, so a retry reuses the same bytes hash.
type employeeMessageResourceReader struct {
	handler  *Handler
	store    *employeeentry.Store
	db       employeeentry.DB
	provider employeeResourceProvider
	limits   employeeresource.Limits
	timeout  time.Duration
}

func newEmployeeMessageResourceReader(w *EmployeeSceneWorker, provider employeeResourceProvider) *employeeMessageResourceReader {
	if w == nil || w.handler == nil {
		return nil
	}
	database, _ := employeeEntryDB(w.handler)
	return &employeeMessageResourceReader{handler: w.handler, store: w.store, db: database, provider: provider, limits: employeeresource.DefaultLimits(), timeout: employeeResourceReadTimeout}
}

// employeeResourceSlot is one item of the context in order: either a frozen
// record (key) or a transient, message-level item that is never stored.
type employeeResourceSlot struct {
	key       employeeResourceKey
	frozen    bool
	transient employeeresource.Item
}

type employeeResourceKey struct {
	receipt, source, message, resource string
}

type employeeResourceRow struct {
	key        employeeResourceKey
	id         string
	requester  string
	relation   employeeresource.Relation
	idType     string
	kind       string
	extraction employeeresource.Extraction
}

// employeeResourceTarget is everything a read is bound to, fixed before any
// provider I/O.
type employeeResourceTarget struct {
	job          employeeentry.Job
	input        dingtalkresponse.ActionInput
	conversation string
}

// Read returns the ResourceContext of the job's source messages. A scope,
// identity, tenant or permission failure is an error before any provider
// I/O; per-resource outcomes are explicit item states.
func (r *employeeMessageResourceReader) Read(ctx context.Context, job employeeentry.Job, envelopes []employeeDispatchEnvelope) (employeeresource.Context, error) {
	out := employeeresource.Context{Version: employeeresource.ContextVersion, Items: []employeeresource.Item{}}
	if r == nil || r.handler == nil || r.store == nil || r.db == nil || r.provider == nil {
		return out, errors.New("employee resource reader is not configured")
	}
	if !employeeWindowMayCarryResources(envelopes) {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	target, sources, err := r.bind(ctx, job, envelopes)
	if err != nil || len(sources) == 0 {
		return out, err
	}
	frozen, err := r.frozen(ctx, job)
	if err != nil {
		return out, err
	}
	var slots []employeeResourceSlot
	var pending []employeeResourceRow
	reads, downloadsLeft, textLeft := 0, r.limits.MaxDownloadsPerRead, r.limits.MaxTextBytes
	readMessage := func(id string) (dwsclient.MessageResources, *employeeresource.Item) {
		if reads >= employeeResourceMaxMessageReads {
			return dwsclient.MessageResources{}, &employeeresource.Item{State: employeeresource.Unavailable, Reason: employeeResourceReasonReadLimit}
		}
		reads++
		message, err := r.provider.ReadMessageResources(ctx, target.input, target.conversation, id)
		switch {
		case err == nil:
			return message, nil
		case ctx.Err() != nil || !errors.Is(err, dwsclient.ErrMessageUnverified):
			return message, &employeeresource.Item{State: employeeresource.Unavailable, Reason: employeeresource.ReasonProviderUnavailable, Retryable: true}
		default:
			return message, &employeeresource.Item{State: employeeresource.Unavailable, Reason: employeeResourceReasonMessageUnverified}
		}
	}
	collect := func(source employeeSourceMessage, relation employeeresource.Relation, message dwsclient.MessageResources) {
		kinds := make([]string, len(message.Resources))
		for i, resource := range message.Resources {
			kinds[i] = employeeresource.Kind(resource.Type)
			if kinds[i] == "file" && resource.IDType != "fileId" {
				kinds[i] = "other"
			}
		}
		verdicts, used := employeeresource.Plan(kinds, r.limits, downloadsLeft)
		downloadsLeft -= used
		for i, resource := range message.Resources {
			key := employeeResourceKey{receipt: source.ReceiptID, source: source.Message.OpenMsgID, message: message.MessageID, resource: resource.ID}
			slots = append(slots, employeeResourceSlot{key: key, frozen: true})
			if row, ok := frozen[key]; ok && row.relation == relation {
				textLeft -= len(row.extraction.Text)
				continue
			}
			row := employeeResourceRow{key: key, requester: source.RequesterRef, relation: relation, idType: resource.IDType, kind: kinds[i]}
			if !verdicts[i].Fetch {
				row.extraction = employeeresource.Extraction{State: verdicts[i].State, Reason: verdicts[i].Reason}
				pending = append(pending, row)
				continue
			}
			file, err := r.provider.DownloadMessageFile(ctx, target.input, resource.ID, r.limits.MaxFileBytes)
			switch {
			case errors.Is(err, dwsclient.ErrMessageFileTooLarge):
				row.extraction = employeeresource.Extraction{State: employeeresource.Unavailable, Reason: employeeresource.ReasonTooLarge}
			case err != nil:
				// Transient: listed for this wake, never frozen as permanent.
				slots[len(slots)-1] = employeeResourceSlot{transient: employeeresource.Item{SourceRef: source.SourceRef, Relation: relation, Kind: kinds[i],
					State: employeeresource.Unavailable, Reason: employeeresource.ReasonProviderUnavailable, Retryable: true}}
				continue
			default:
				row.extraction = employeeresource.Extract(file.Name, file.ContentType, file.Data, textLeft, r.limits)
				textLeft -= len(row.extraction.Text)
			}
			pending = append(pending, row)
		}
	}
	for _, source := range sources {
		message, failure := readMessage(source.Message.OpenMsgID)
		if failure != nil {
			slots = append(slots, employeeResourceSlot{transient: r.messageItem(source, employeeresource.Current, *failure)})
			continue
		}
		if message.SenderOpenDingTalkID != strings.TrimSpace(source.Message.SenderOpenDingTalkID) {
			// The provider's sender is not the frozen requester.
			slots = append(slots, employeeResourceSlot{transient: r.messageItem(source, employeeresource.Current, employeeresource.Item{State: employeeresource.Unavailable, Reason: employeeresource.ReasonRequesterMismatch})})
			continue
		}
		collect(source, employeeresource.Current, message)
		reference := source.Message.ReferencedMessage
		if reference == nil || strings.TrimSpace(reference.OpenMsgID) == "" {
			// A provider quote without an outer reference carries no intent.
			continue
		}
		quotedID := strings.TrimSpace(reference.OpenMsgID)
		if message.Quoted == nil || message.Quoted.MessageID != quotedID || message.Quoted.ConversationID != target.conversation {
			slots = append(slots, employeeResourceSlot{transient: r.messageItem(source, employeeresource.Quoted, employeeresource.Item{State: employeeresource.Unavailable, Reason: employeeresource.ReasonQuoteUnverified})})
			continue
		}
		quoted, failure := readMessage(quotedID)
		if failure != nil {
			slots = append(slots, employeeResourceSlot{transient: r.messageItem(source, employeeresource.Quoted, *failure)})
			continue
		}
		collect(source, employeeresource.Quoted, quoted)
	}
	if len(pending) > 0 {
		if err = r.save(ctx, target, pending); err != nil {
			return out, err
		}
		if frozen, err = r.frozen(ctx, job); err != nil {
			return out, err
		}
	}
	for _, slot := range slots {
		item := slot.transient
		if slot.frozen {
			row, ok := frozen[slot.key]
			if !ok {
				return out, errors.New("employee resource record is missing after save")
			}
			item = row.item()
		}
		if out.TextBytes+len(item.Text) > r.limits.MaxTextBytes {
			// Frozen text from an earlier, differently ordered attempt never
			// widens this wake's foreground budget.
			end := employeeresource.TruncateUTF8(item.Text, r.limits.MaxTextBytes-out.TextBytes)
			item.Text = item.Text[:end]
			if item.Range != nil {
				item.Range.End = item.Range.Start + end
			}
			item.State, item.Reason = employeeresource.Partial, employeeresource.ReasonTextTruncated
		}
		out.TextBytes += len(item.Text)
		out.Items = append(out.Items, item)
		slog.InfoContext(ctx, "employee message resource", "event", "employee_message_resource", "job_id", job.ID, "agent_id", job.Scope.AgentID,
			"scene_id", job.Scope.SceneID, "source_ref", item.SourceRef, "resource_ref", item.Ref, "relation", item.Relation, "kind", item.Kind,
			"state", item.State, "reason", item.Reason, "size_bytes", item.SizeBytes, "sha256", item.SHA256, "text_bytes", len(item.Text), "retryable", item.Retryable)
	}
	return out, nil
}

func (r *employeeMessageResourceReader) messageItem(source employeeSourceMessage, relation employeeresource.Relation, item employeeresource.Item) employeeresource.Item {
	item.SourceRef, item.Relation, item.Kind = source.SourceRef, relation, "unknown"
	return item
}

// bind verifies every frozen envelope against the claimed job, the scene
// directory, the agent's current DWS identity and the admission principal,
// and returns the source messages that may carry resources.
func (r *employeeMessageResourceReader) bind(ctx context.Context, job employeeentry.Job, envelopes []employeeDispatchEnvelope) (employeeResourceTarget, []employeeSourceMessage, error) {
	if len(job.Items) == 0 || len(envelopes) != len(job.Items) {
		return employeeResourceTarget{}, nil, errEmployeeResourceScope
	}
	registered, err := employeeSceneFence(ctx, r.handler, job)
	if err != nil {
		return employeeResourceTarget{}, nil, fmt.Errorf("employee resource scene fence: %w", err)
	}
	if registered.SceneKind != scene.KindGroup && registered.SceneKind != scene.KindDM {
		return employeeResourceTarget{}, nil, errEmployeeResourceScope
	}
	if err = employeePrincipalAllowed(ctx, r.handler, job.Scope, job.PrincipalID); err != nil {
		return employeeResourceTarget{}, nil, fmt.Errorf("employee resource permission: %w", err)
	}
	identity, err := r.handler.Queries.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: parseUUID(job.Scope.WorkspaceID), AgentID: parseUUID(job.Scope.AgentID)})
	if err != nil {
		return employeeResourceTarget{}, nil, fmt.Errorf("employee resource identity: %w", err)
	}
	target := employeeResourceTarget{job: job, conversation: registered.ExternalSceneID}
	var sources []employeeSourceMessage
	for i, item := range job.Items {
		env := envelopes[i]
		command := env.Command
		if item.PrincipalID != job.PrincipalID || env.PrincipalID != job.PrincipalID || command.EventReceiptID != item.ReceiptID ||
			dispatchSceneID(command) != job.Scope.SceneID || dispatchRecordedOrg(command) != job.Scope.TenantOrgID ||
			strings.TrimSpace(command.Event.Data.Conversation.OpenConversationID) != registered.ExternalSceneID {
			return employeeResourceTarget{}, nil, errEmployeeResourceScope
		}
		if command.ExternalIdentity.DWS == nil || command.ExternalIdentity.DWS.UID != identity.DwsUid {
			return employeeResourceTarget{}, nil, errors.New("employee resource identity changed")
		}
		if i == 0 {
			target.input = dingtalkresponse.ActionInput{WorkspaceID: job.Scope.WorkspaceID, AgentID: job.Scope.AgentID, RequestID: "employee-resource:" + job.ID,
				DWSUID: identity.DwsUid, DWSOrgID: job.Scope.TenantOrgID, SceneID: job.Scope.SceneID, ConversationID: registered.ExternalSceneID,
				IsGroup: registered.SceneKind == scene.KindGroup, DWSEnvironment: commandDWSEnvironment(command)}
		}
		for _, source := range employeeSourceMessages(item, env) {
			if source.SourceRef != "" && source.RequesterRef != "" && source.Message.Reaction == nil && employeeMessageMayCarryResources(source.Message) {
				sources = append(sources, source)
			}
		}
	}
	return target, sources, nil
}

func employeeWindowMayCarryResources(envelopes []employeeDispatchEnvelope) bool {
	for _, env := range envelopes {
		for _, m := range env.Command.Event.Data.Messages {
			if m.Reaction == nil && employeeMessageMayCarryResources(m) {
				return true
			}
		}
	}
	return false
}

// employeeMessageMayCarryResources is only a hint to spend a provider read:
// DingTalk renders a file or media message with this notation, and a quote
// names its target. It never decides what a message carries.
func employeeMessageMayCarryResources(m DispatchMessage) bool {
	if m.ReferencedMessage != nil || len(m.Attachments) > 0 {
		return true
	}
	text := strings.ToLower(m.Text)
	for _, marker := range []string{"fileid", "mediaid", "[文件]", "[图片", "[视频", "[语音"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

const employeeResourceColumns = `id::text,receipt_id::text,source_message_id,message_id,resource_id,relation,resource_id_type,resource_kind,state,reason,file_name,media_type,size_bytes,content_sha256,extracted_text,extract_start,extract_end,extract_total`

// frozen reads the job's frozen records, bound to its exact scope and
// requester; a record of another scope or principal is never reused.
func (r *employeeMessageResourceReader) frozen(ctx context.Context, job employeeentry.Job) (map[employeeResourceKey]employeeResourceRow, error) {
	receipts := make([]string, len(job.Items))
	for i, item := range job.Items {
		receipts[i] = item.ReceiptID
	}
	rows, err := r.db.Query(ctx, `SELECT `+employeeResourceColumns+` FROM employee_message_resource WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid AND principal_id=$5::uuid AND receipt_id=ANY($6::uuid[])`,
		job.Scope.WorkspaceID, job.Scope.AgentID, job.Scope.TenantOrgID, job.Scope.SceneID, job.PrincipalID, receipts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[employeeResourceKey]employeeResourceRow{}
	for rows.Next() {
		var row employeeResourceRow
		var start, end, total int
		var state, relation string
		e := &row.extraction
		if err = rows.Scan(&row.id, &row.key.receipt, &row.key.source, &row.key.message, &row.key.resource, &relation, &row.idType, &row.kind, &state, &e.Reason, &e.Name, &e.MediaType, &e.SizeBytes, &e.SHA256, &e.Text, &start, &end, &total); err != nil {
			return nil, err
		}
		row.relation, e.State = employeeresource.Relation(relation), employeeresource.State(state)
		if e.State == employeeresource.Available || e.State == employeeresource.Partial {
			e.Range = &employeeresource.Range{Start: start, End: end, Total: total}
		}
		out[row.key] = row
	}
	return out, rows.Err()
}

// save freezes new records under the current job lease, after rechecking the
// scene fence and the admission principal. The first record of a key wins.
func (r *employeeMessageResourceReader) save(ctx context.Context, target employeeResourceTarget, rows []employeeResourceRow) error {
	job := target.job
	return r.store.WithLease(ctx, job, func(tx pgx.Tx) error {
		if _, err := employeeSceneFence(ctx, r.handler, job); err != nil {
			return fmt.Errorf("employee resource scene fence: %w", err)
		}
		if err := employeePrincipalAllowed(ctx, r.handler, job.Scope, job.PrincipalID); err != nil {
			return fmt.Errorf("employee resource permission: %w", err)
		}
		for _, row := range rows {
			if row.requester == "" {
				return errEmployeeResourceScope
			}
			e := row.extraction
			start, end, total := 0, 0, 0
			if e.Range != nil {
				start, end, total = e.Range.Start, e.Range.End, e.Range.Total
			}
			if _, err := tx.Exec(ctx, `INSERT INTO employee_message_resource(workspace_id,agent_id,tenant_org_id,scene_id,receipt_id,source_message_id,requester_ref,principal_id,relation,message_id,resource_id,resource_id_type,resource_kind,state,reason,file_name,media_type,size_bytes,content_sha256,extracted_text,extract_start,extract_end,extract_total)
VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6,$7,$8::uuid,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
ON CONFLICT (workspace_id,agent_id,receipt_id,source_message_id,message_id,resource_id) DO NOTHING`,
				job.Scope.WorkspaceID, job.Scope.AgentID, job.Scope.TenantOrgID, job.Scope.SceneID, row.key.receipt, row.key.source, row.requester, job.PrincipalID,
				string(row.relation), row.key.message, row.key.resource, row.idType, row.kind, string(e.State), e.Reason, e.Name, e.MediaType, e.SizeBytes, e.SHA256, e.Text, start, end, total); err != nil {
				return err
			}
		}
		return nil
	})
}

func (row employeeResourceRow) item() employeeresource.Item {
	e := row.extraction
	item := employeeresource.Item{Ref: row.id, SourceRef: row.key.receipt + "/" + row.key.source, Relation: row.relation, Kind: row.kind, Name: e.Name,
		MediaType: e.MediaType, State: e.State, Reason: e.Reason, SizeBytes: e.SizeBytes, SHA256: e.SHA256, Text: e.Text}
	if e.Range != nil {
		copied := *e.Range
		item.Range = &copied
	}
	return item
}
