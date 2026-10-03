package handler

// Webhook endpoint binding and frozen source.
//
// An endpoint is the webhook trigger found by the token in its URL. The
// contract every accepted delivery satisfies:
//
//   - Binding. Where an event goes is read from PostgreSQL only: the
//     trigger's autopilot and workspace, its assignee, and for a scene routine
//     (例行任务) the routine's scene, scene kind and tenant org, plus the
//     creator, the execution mode (dispatch selection), the signature policy
//     with the secret revision, and the payload policy. The binding is frozen
//     on the delivery (source_binding). Actor, org, scene, agent or mode
//     fields inside the body are data handed to the run; nothing here reads
//     them. A routine whose row disagrees with its autopilot (workspace or
//     agent) is unusable and accepts nothing.
//   - Signature. HMAC-SHA256 over the exact bytes received
//     (X-Hub-Signature-256), checked before event identity, so an
//     unauthenticated request can neither probe a known event id nor bump
//     it. The supported protocols (github, generic) carry no timestamp, so
//     no expiry is invented; replay protection is the event id. A valid
//     signature only proves the sender; the scene and tenant are still
//     fenced when the run is admitted.
//   - Identity. With a provider event id (X-GitHub-Delivery for github;
//     Idempotency-Key, else X-GitHub-Delivery for generic) the event is
//     (trigger, exact id), held by the first authenticated delivery whatever
//     its later status (a failed delivery keeps it; Replay runs it again):
//     the same id with the same effective payload returns that receipt, a
//     different effective payload is a 409 conflict. Rejected rows
//     (signature failures, conflict audits) never hold an identity. Without
//     an id every request is its own event (per_request); identical bodies
//     are never collapsed by a content hash.
//   - Target. An admitted run is dispatched only to the target frozen at
//     acceptance; if the assignee, mode, routine scene or tenant changed, or
//     the routine scene fails the tenant fence, the run is skipped and the
//     delivery ignored (binding_changed / scene_unusable), never re-routed.
//   - Frozen input. The envelope's receivedAt is the delivery row's
//     received_at and the effective payload digest is stored with the row, so
//     a worker retry rebuilds byte-identical input or fails closed
//     (source_digest_mismatch); a binding that changed before admission runs
//     nowhere (binding_changed). An admitted run keeps the payload and route
//     it was admitted with.
//   - Responses only say accepted / skipped / ignored / duplicate / rejected
//     / conflict; queued work is never reported as done. Logs carry ids and
//     outcomes, never the body, the secret or the token.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	webhookBindingVersion = 1

	// Identity policies of a delivery.
	webhookIdentityProviderEventID = "provider_event_id"
	webhookIdentityPerRequest      = "per_request"

	// Signature policies of an endpoint.
	webhookSignatureNone      = "none"
	webhookSignatureHubSHA256 = "hub_hmac_sha256"

	// webhookPayloadEnvelopeV1 hands the run the normalized envelope (event,
	// eventPayload, receivedAt, contentType) within the ingress body cap.
	webhookPayloadEnvelopeV1 = "envelope_v1"

	// Dispatch selections. The Employee producer adds its own value.
	webhookDispatchAutopilotRunOnly     = "autopilot_run_only"
	webhookDispatchAutopilotCreateIssue = "autopilot_create_issue"

	// webhookBindingInvalid is the ignored reason of an endpoint whose
	// routine disagrees with its autopilot.
	webhookBindingInvalid = "binding_invalid"
	// webhookBindingChanged is the ignored reason of a delivery whose route
	// changed between acceptance and admission.
	webhookBindingChanged = "binding_changed"
	// webhookSourceDigestMismatch is the failure of a delivery whose stored
	// body no longer rebuilds the accepted input.
	webhookSourceDigestMismatch = "source_digest_mismatch"
	// webhookEventIDConflict is the rejection of a reused event id with a
	// different effective payload.
	webhookEventIDConflict = "event_id_conflict"

	// maxWebhookEventIDBytes bounds a provider event id: ids are UUIDs or
	// short tokens, and the dedupe index cannot take arbitrary header sizes.
	maxWebhookEventIDBytes = 255
)

var (
	// errWebhookSourceDrift reports that a delivery's stored body no longer
	// rebuilds the effective payload it was accepted with.
	errWebhookSourceDrift = errors.New("webhook source digest mismatch")
	// errWebhookStoredBodyInvalid reports a stored body of an older delivery
	// (no frozen digest) that no longer normalizes.
	errWebhookStoredBodyInvalid = errors.New("normalize stored body")
)

// WebhookEndpointBinding is the trusted binding of a webhook endpoint at the
// time a delivery was accepted.
type WebhookEndpointBinding struct {
	Version       int    `json:"v"`
	WorkspaceID   string `json:"workspace_id"`
	AutopilotID   string `json:"autopilot_id"`
	TriggerID     string `json:"trigger_id"`
	Provider      string `json:"provider"`
	AssigneeType  string `json:"assignee_type"`
	AssigneeID    string `json:"assignee_id"`
	ExecutionMode string `json:"execution_mode"`
	Dispatch      string `json:"dispatch"`
	// CreatorType/CreatorID are the routine's creator for a scene routine,
	// else the autopilot's. Authority is checked by the consumer at use time.
	CreatorType string `json:"creator_type"`
	CreatorID   string `json:"creator_id"`
	// RoutineID/SceneID/SceneKind/TenantOrgID are set for a scene routine.
	RoutineID       string `json:"routine_id,omitempty"`
	SceneID         string `json:"scene_id,omitempty"`
	SceneKind       string `json:"scene_kind,omitempty"`
	TenantOrgID     string `json:"tenant_org_id,omitempty"`
	SignaturePolicy string `json:"signature_policy"`
	SecretRevision  string `json:"secret_revision,omitempty"`
	PayloadPolicy   string `json:"payload_policy"`
}

// routeKey is the part of a binding that decides where work goes.
func (b WebhookEndpointBinding) routeKey() string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%s", b.WorkspaceID, b.AutopilotID, b.AssigneeType, b.AssigneeID,
		b.ExecutionMode, b.RoutineID, b.SceneID, b.SceneKind, b.TenantOrgID)
}

// WebhookFrozenSource is the input a delivery was accepted with, rebuilt
// from its row: the identity, the first receivedAt, the effective payload
// digest, the envelope bytes the run is admitted with and the binding.
type WebhookFrozenSource struct {
	DeliveryID     string
	TriggerID      string
	IdentityPolicy string
	EventID        string
	EventIDSource  string
	ReceivedAt     time.Time
	Digest         string
	Envelope       WebhookEnvelope
	Payload        json.RawMessage
	// Binding is zero (Version 0) for a delivery accepted by an older binary.
	Binding WebhookEndpointBinding
}

// resolveWebhookEndpointBinding reads the endpoint's binding from
// PostgreSQL. problem is non-empty when the endpoint must not accept work.
func (h *Handler) resolveWebhookEndpointBinding(ctx context.Context, ap db.Autopilot, triggerID pgtype.UUID, provider, signingSecret string) (b WebhookEndpointBinding, problem string, err error) {
	b = WebhookEndpointBinding{
		Version:         webhookBindingVersion,
		WorkspaceID:     uuidToString(ap.WorkspaceID),
		AutopilotID:     uuidToString(ap.ID),
		TriggerID:       uuidToString(triggerID),
		Provider:        provider,
		AssigneeType:    ap.AssigneeType,
		AssigneeID:      uuidToString(ap.AssigneeID),
		ExecutionMode:   ap.ExecutionMode,
		CreatorType:     ap.CreatedByType,
		CreatorID:       uuidToString(ap.CreatedByID),
		SignaturePolicy: webhookSignatureNone,
		PayloadPolicy:   webhookPayloadEnvelopeV1,
	}
	switch ap.ExecutionMode {
	case "run_only":
		b.Dispatch = webhookDispatchAutopilotRunOnly
	case "create_issue":
		b.Dispatch = webhookDispatchAutopilotCreateIssue
	default:
		b.Dispatch = "autopilot_" + ap.ExecutionMode
	}
	if signingSecret != "" {
		b.SignaturePolicy = webhookSignatureHubSHA256
		b.SecretRevision = webhookSecretRevision(signingSecret)
	}
	if h.DB == nil {
		return b, "", nil
	}
	routine, err := contextcap.GetRoutineByAutopilot(ctx, h.DB, b.AutopilotID)
	if errors.Is(err, contextcap.ErrNotFound) {
		return b, "", nil
	}
	if err != nil {
		return b, "", fmt.Errorf("load scene routine: %w", err)
	}
	b.RoutineID, b.SceneID, b.SceneKind, b.TenantOrgID = routine.ID, routine.SceneID, routine.SceneKind, routine.TenantOrgID
	b.CreatorType, b.CreatorID = routine.CreatedByType, routine.CreatedByID
	if routine.WorkspaceID != b.WorkspaceID || ap.AssigneeType != "agent" || routine.AgentID != b.AssigneeID {
		return b, webhookBindingInvalid, nil
	}
	return b, "", nil
}

// webhookSecretRevision fingerprints a signing secret so a delivery records
// which secret verified it without storing or logging the secret.
func webhookSecretRevision(secret string) string {
	sum := sha256.Sum256([]byte("multica.webhook.signing_secret/v1\x00" + secret))
	return "sha256:" + hex.EncodeToString(sum[:8])
}

// webhookIdentityPolicy names how a delivery's event identity is decided.
func webhookIdentityPolicy(eventID string) string {
	if eventID == "" {
		return webhookIdentityPerRequest
	}
	return webhookIdentityProviderEventID
}

// validWebhookEventID accepts a bounded, printable provider event id.
func validWebhookEventID(id string) bool {
	if id == "" || len(id) > maxWebhookEventIDBytes || !utf8.ValidString(id) {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// webhookSourceDigest fingerprints the effective payload of an envelope: the
// event name and the canonical JSON of its event payload. Key order,
// whitespace and the request metadata (receivedAt, contentType) do not
// count.
func webhookSourceDigest(env WebhookEnvelope) (string, error) {
	canonical, err := canonicalWebhookJSON(env.EventPayload)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte("multica.webhook.source/v1\x00"))
	h.Write([]byte(env.Event))
	h.Write([]byte{0})
	h.Write(canonical)
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// canonicalWebhookJSON re-encodes JSON with sorted object keys, no
// insignificant whitespace and numbers kept as written.
func canonicalWebhookJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonical json: %w", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("canonical json: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// webhookReceivedAt formats the frozen receivedAt of an envelope.
func webhookReceivedAt(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// webhookEnvelopeFromDelivery builds the envelope of a stored delivery: the
// stored body and selected headers, with the delivery's own received_at. The
// ingress and the worker both use it, so they produce the same bytes.
func webhookEnvelopeFromDelivery(delivery db.WebhookDelivery) (WebhookEnvelope, error) {
	headers := headersFromSelected(delivery.SelectedHeaders)
	if delivery.ContentType.Valid {
		headers.Set("Content-Type", delivery.ContentType.String)
	}
	env, err := normalizeWebhookPayload(delivery.RawBody, headers)
	if err != nil {
		return WebhookEnvelope{}, err
	}
	if delivery.ReceivedAt.Valid {
		env.Request.ReceivedAt = webhookReceivedAt(delivery.ReceivedAt.Time)
	}
	return env, nil
}

// webhookFrozenColumns are the source columns written with a delivery.
type webhookFrozenColumns struct {
	Digest         string
	SecretRevision string
	Binding        []byte
}

// webhookEventIDTaken reports that an authenticated delivery already holds
// the event id; ID is that delivery.
type webhookEventIDTaken struct{ ID pgtype.UUID }

func (e *webhookEventIDTaken) Error() string { return "webhook event id already accepted" }

// insertWebhookDelivery inserts a delivery and its frozen source in one
// transaction. An authenticated delivery with an event id first claims the
// identity: under a transaction advisory lock on (trigger, event id) it looks
// for any earlier delivery with that id that was not rejected — whatever its
// later dispatch status, failed included — and returns webhookEventIDTaken
// instead of inserting. Rejected rows (signature failures, conflict audits)
// never hold an identity. A unique violation from an older binary racing on
// the partial dedupe index is returned as is.
func (h *Handler) insertWebhookDelivery(ctx context.Context, params db.CreateWebhookDeliveryParams, frozen webhookFrozenColumns) (db.WebhookDelivery, error) {
	if h.TxStarter == nil {
		return db.WebhookDelivery{}, errors.New("webhook delivery: no transaction starter")
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return db.WebhookDelivery{}, err
	}
	defer tx.Rollback(ctx)
	if params.DedupeKey.Valid && params.Status != deliveryStatusRejected {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('webhook_event_identity:' || $1::text || ':' || $2, 0))`,
			params.TriggerID, params.DedupeKey.String); err != nil {
			return db.WebhookDelivery{}, fmt.Errorf("lock event identity: %w", err)
		}
		var holder pgtype.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM webhook_delivery
			WHERE trigger_id = $1 AND dedupe_key = $2 AND status <> 'rejected'
			ORDER BY (status = 'failed'), created_at DESC
			LIMIT 1`, params.TriggerID, params.DedupeKey.String).Scan(&holder)
		if err == nil {
			return db.WebhookDelivery{}, &webhookEventIDTaken{ID: holder}
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return db.WebhookDelivery{}, fmt.Errorf("look up event identity: %w", err)
		}
	}
	delivery, err := h.Queries.WithTx(tx).CreateWebhookDelivery(ctx, params)
	if err != nil {
		return db.WebhookDelivery{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE webhook_delivery
		SET source_digest = $2, signing_secret_revision = NULLIF($3, ''), source_binding = $4::jsonb
		WHERE id = $1`, delivery.ID, frozen.Digest, frozen.SecretRevision, frozen.Binding); err != nil {
		return db.WebhookDelivery{}, fmt.Errorf("record frozen source: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.WebhookDelivery{}, err
	}
	return delivery, nil
}

// readWebhookFrozenColumns reads a delivery's frozen source columns; all
// are empty for a delivery written by an older binary.
func (h *Handler) readWebhookFrozenColumns(ctx context.Context, id pgtype.UUID) (webhookFrozenColumns, error) {
	var digest, revision pgtype.Text
	var binding []byte
	err := h.DB.QueryRow(ctx, `SELECT source_digest, signing_secret_revision, source_binding FROM webhook_delivery WHERE id = $1`, id).
		Scan(&digest, &revision, &binding)
	if err != nil {
		return webhookFrozenColumns{}, err
	}
	return webhookFrozenColumns{Digest: digest.String, SecretRevision: revision.String, Binding: binding}, nil
}

// webhookStoredDigest returns the effective payload digest of a stored
// delivery: the frozen one, or for an older row the one its body rebuilds.
func (h *Handler) webhookStoredDigest(ctx context.Context, delivery db.WebhookDelivery) (string, error) {
	frozen, err := h.readWebhookFrozenColumns(ctx, delivery.ID)
	if err != nil {
		return "", err
	}
	if frozen.Digest != "" {
		return frozen.Digest, nil
	}
	env, err := webhookEnvelopeFromDelivery(delivery)
	if err != nil {
		return "", fmt.Errorf("rebuild stored delivery: %w", err)
	}
	return webhookSourceDigest(env)
}

// loadWebhookFrozenSource rebuilds the input a delivery was accepted with.
// It returns errWebhookSourceDrift when the stored body no longer produces
// the accepted effective payload.
func (h *Handler) loadWebhookFrozenSource(ctx context.Context, delivery db.WebhookDelivery) (WebhookFrozenSource, error) {
	frozen, err := h.readWebhookFrozenColumns(ctx, delivery.ID)
	if err != nil {
		return WebhookFrozenSource{}, fmt.Errorf("load frozen source: %w", err)
	}
	env, err := webhookEnvelopeFromDelivery(delivery)
	if err != nil {
		if frozen.Digest != "" {
			return WebhookFrozenSource{}, fmt.Errorf("%w: stored body no longer normalizes", errWebhookSourceDrift)
		}
		return WebhookFrozenSource{}, fmt.Errorf("%w: %v", errWebhookStoredBodyInvalid, err)
	}
	digest, err := webhookSourceDigest(env)
	if err != nil {
		return WebhookFrozenSource{}, fmt.Errorf("digest stored body: %w", err)
	}
	if frozen.Digest != "" && frozen.Digest != digest {
		return WebhookFrozenSource{}, errWebhookSourceDrift
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return WebhookFrozenSource{}, fmt.Errorf("encode envelope: %w", err)
	}
	src := WebhookFrozenSource{
		DeliveryID:     uuidToString(delivery.ID),
		TriggerID:      uuidToString(delivery.TriggerID),
		IdentityPolicy: webhookIdentityPolicy(delivery.DedupeKey.String),
		EventID:        delivery.DedupeKey.String,
		EventIDSource:  delivery.DedupeSource.String,
		ReceivedAt:     delivery.ReceivedAt.Time,
		Digest:         digest,
		Envelope:       env,
		Payload:        payload,
	}
	if len(frozen.Binding) > 0 {
		if err := json.Unmarshal(frozen.Binding, &src.Binding); err != nil {
			return WebhookFrozenSource{}, fmt.Errorf("decode frozen binding: %w", err)
		}
	}
	return src, nil
}

// webhookSceneUnusable is the refusal of an admitted routine run whose scene
// can no longer be used by the agent in the routine's tenant.
const webhookSceneUnusable = "scene_unusable"

// admittedWebhookRunAwaitsDispatch reports whether an admitted run has not
// reached its downstream issue or task yet, so dispatching it would still
// choose a target.
func (h *Handler) admittedWebhookRunAwaitsDispatch(ctx context.Context, run db.AutopilotRun) (bool, error) {
	switch run.Status {
	case "completed", "failed", "skipped":
		return false, nil
	case "issue_created":
		return !run.IssueID.Valid, nil
	case "running":
		if run.TaskID.Valid {
			return false, nil
		}
		_, err := h.Queries.GetAutopilotTaskByRun(ctx, run.ID)
		if err == nil {
			return false, nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return true, nil
		}
		return false, fmt.Errorf("load admitted run task: %w", err)
	}
	return true, nil
}

// admittedWebhookRunRefusal decides whether an admitted, not yet dispatched
// run may still run on the target frozen at acceptance. It returns
// binding_changed when the current binding routes elsewhere (assignee, mode,
// routine, scene, tenant) or became invalid, scene_unusable when the
// routine's scene fails the use-time tenant fence, and "" when the current
// target is the frozen one. A delivery accepted by an older binary has no
// frozen binding and is dispatched as before.
func (h *Handler) admittedWebhookRunRefusal(ctx context.Context, delivery db.WebhookDelivery, ap db.Autopilot, trigger db.AutopilotTrigger) (string, error) {
	frozen, err := h.readWebhookFrozenColumns(ctx, delivery.ID)
	if err != nil {
		return "", fmt.Errorf("load frozen binding: %w", err)
	}
	if len(frozen.Binding) == 0 {
		return "", nil
	}
	var accepted WebhookEndpointBinding
	if err := json.Unmarshal(frozen.Binding, &accepted); err != nil {
		return "", fmt.Errorf("decode frozen binding: %w", err)
	}
	if accepted.Version == 0 {
		return "", nil
	}
	current, problem, err := h.resolveWebhookEndpointBinding(ctx, ap, trigger.ID, delivery.Provider, trigger.SigningSecret.String)
	if err != nil {
		return "", err
	}
	if problem != "" || current.routeKey() != accepted.routeKey() {
		return webhookBindingChanged, nil
	}
	if accepted.RoutineID == "" {
		return "", nil
	}
	routine, err := contextcap.GetRoutineByAutopilot(ctx, h.DB, accepted.AutopilotID)
	if errors.Is(err, contextcap.ErrNotFound) {
		return webhookBindingChanged, nil
	}
	if err != nil {
		return "", fmt.Errorf("load scene routine: %w", err)
	}
	if err := h.routineSceneUsable(ctx, routine); err != nil {
		if errors.Is(err, service.ErrSceneRoutineUnusable) {
			return webhookSceneUnusable, nil
		}
		return "", err
	}
	return "", nil
}

// webhookConflictResponse is the 409 body of a reused event id with a
// different effective payload.
func webhookConflictResponse(auditID pgtype.UUID) (int, map[string]any) {
	return http.StatusConflict, map[string]any{
		"status":      "conflict",
		"delivery_id": uuidToString(auditID),
		"reason":      webhookEventIDConflict,
	}
}

// logWebhookOutcome records how the ingress answered a persisted delivery:
// ids, identity, signature and route. Never the body, the event id, the
// secret or the token.
func logWebhookOutcome(ctx context.Context, delivery db.WebhookDelivery, b WebhookEndpointBinding, digest string, code int, resp map[string]any) {
	attrs := []any{
		"http_status", code,
		"outcome", resp["status"],
		"delivery_id", uuidToString(delivery.ID),
		"trigger_id", b.TriggerID,
		"autopilot_id", b.AutopilotID,
		"assignee_id", b.AssigneeID,
		"dispatch", b.Dispatch,
		"identity_policy", webhookIdentityPolicy(delivery.DedupeKey.String),
		"event_id_source", delivery.DedupeSource.String,
		"signature", delivery.SignatureStatus,
		"source_digest", shortWebhookDigest(digest),
	}
	if b.SecretRevision != "" {
		attrs = append(attrs, "secret_revision", b.SecretRevision)
	}
	if b.RoutineID != "" {
		attrs = append(attrs, "routine_id", b.RoutineID, "scene_id", b.SceneID, "tenant_org_id", b.TenantOrgID)
	}
	if v, ok := resp["run_id"].(string); ok {
		attrs = append(attrs, "run_id", v)
	}
	if v, ok := resp["reason"].(string); ok {
		attrs = append(attrs, "reason", v)
	}
	slog.InfoContext(ctx, "webhook delivery answered", attrs...)
}

// shortWebhookDigest keeps enough of a digest to correlate deliveries.
func shortWebhookDigest(digest string) string {
	const keep = len("sha256:") + 12
	if len(digest) > keep {
		return digest[:keep]
	}
	return digest
}
