package handler

import (
	"bytes"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dshtrajectory"
)

const (
	maxDSHTrajectoryBytes         int64 = 32 << 20
	dshTrajectorySealOverhead           = 28
	dshTrajectoryEncryptionScheme       = "aes-256-gcm-v1"
	maxJSONSafeInteger            int64 = 1<<53 - 1
)

var dshSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type dshTrajectoryHeader struct {
	Type            string `json:"type"`
	Version         int32  `json:"version"`
	ID              string `json:"id"`
	CreatedAt       int64  `json:"createdAt"`
	DelegationDepth int32  `json:"delegationDepth"`
}

type dshTrajectoryEvent struct {
	Type string          `json:"type"`
	Seq  *int64          `json:"seq"`
	Time *int64          `json:"time"`
	Data json.RawMessage `json:"data"`
}

func validateDSHTrajectory(data []byte, expectedSessionID string) (dshTrajectoryHeader, int32, error) {
	if dshtrajectory.IsRange(data) {
		doc, err := dshtrajectory.Parse(data)
		if err != nil {
			return dshTrajectoryHeader{}, 0, err
		}
		if doc.Scope.SessionID != expectedSessionID {
			return dshTrajectoryHeader{}, 0, errors.New("session id does not match trajectory scope")
		}
		var header dshTrajectoryHeader
		if err := json.Unmarshal(doc.Header, &header); err != nil {
			return header, 0, err
		}
		return header, int32(len(doc.Events)), nil
	}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) == 0 || len(bytes.TrimSpace(lines[0])) == 0 {
		return dshTrajectoryHeader{}, 0, errors.New("invalid session header")
	}
	var rawHeader struct {
		Type            string `json:"type"`
		Version         *int32 `json:"version"`
		IsSeeded        *bool  `json:"isSeeded"`
		ID              string `json:"id"`
		CreatedAt       *int64 `json:"createdAt"`
		ParentSession   string `json:"parentSession"`
		Origin          string `json:"origin"`
		DelegationDepth *int32 `json:"delegationDepth"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(lines[0]), &rawHeader); err != nil {
		return dshTrajectoryHeader{}, 0, fmt.Errorf("invalid session header: %w", err)
	}
	// v3 is the physical JSONL header emitted by the pinned native Host.
	// Keep v0 readable for existing tasks. Seeded and delegated ledgers need a
	// separately authorized lineage, so this task-root endpoint rejects them.
	supportedVersion := rawHeader.Version != nil && (*rawHeader.Version == 0 ||
		(*rawHeader.Version == 3 && rawHeader.IsSeeded != nil && !*rawHeader.IsSeeded))
	var headerFields map[string]json.RawMessage
	_ = json.Unmarshal(bytes.TrimSpace(lines[0]), &headerFields)
	_, depthPresent := headerFields["delegationDepth"]
	validDepth := rawHeader.DelegationDepth != nil && *rawHeader.DelegationDepth == 0 ||
		!depthPresent && rawHeader.Version != nil && *rawHeader.Version == 3
	if rawHeader.Type != "session" || !supportedVersion ||
		rawHeader.CreatedAt == nil || *rawHeader.CreatedAt < 0 || *rawHeader.CreatedAt > maxJSONSafeInteger ||
		!validDepth ||
		rawHeader.ParentSession != "" || rawHeader.Origin != "" {
		return dshTrajectoryHeader{}, 0, errors.New("invalid session header")
	}
	if !dshSessionIDPattern.MatchString(rawHeader.ID) || rawHeader.ID != expectedSessionID {
		return dshTrajectoryHeader{}, 0, errors.New("session id does not match trajectory header")
	}
	header := dshTrajectoryHeader{
		Type:            rawHeader.Type,
		Version:         *rawHeader.Version,
		ID:              rawHeader.ID,
		CreatedAt:       *rawHeader.CreatedAt,
		DelegationDepth: 0,
	}

	var eventCount int32
	var expectedSeq int64
	for lineIndex := 1; lineIndex < len(lines); lineIndex++ {
		line := bytes.TrimSpace(lines[lineIndex])
		if len(line) == 0 {
			continue
		}
		var event dshTrajectoryEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return dshTrajectoryHeader{}, 0, fmt.Errorf("invalid trajectory event: %w", err)
		}
		trimmedData := bytes.TrimSpace(event.Data)
		if event.Type == "" || event.Type == "session" || event.Seq == nil || event.Time == nil ||
			len(trimmedData) < 2 || trimmedData[0] != '{' || trimmedData[len(trimmedData)-1] != '}' {
			return dshTrajectoryHeader{}, 0, errors.New("invalid trajectory event envelope")
		}
		if *event.Seq != expectedSeq || *event.Time < 0 || *event.Time > maxJSONSafeInteger {
			return dshTrajectoryHeader{}, 0, errors.New("trajectory event sequence or timestamp is invalid")
		}
		if eventCount == int32(^uint32(0)>>1) {
			return dshTrajectoryHeader{}, 0, errors.New("trajectory contains too many events")
		}
		eventCount++
		expectedSeq++
	}
	return header, eventCount, nil
}

func validateDSHUploadBinding(data []byte, sessionID, requestID string, bound bool) error {
	if dshtrajectory.IsRange(data) {
		doc, err := dshtrajectory.Parse(data)
		if err != nil {
			return err
		}
		if !bound || sessionID != doc.Scope.SessionID || requestID != doc.Scope.RequestID {
			return errors.New("trajectory does not match the persisted DSH task binding")
		}
	} else if bound {
		return errors.New("managed DSH tasks require a task-scoped trajectory")
	}
	return nil
}

func sealDSHTrajectory(data []byte) (sealed, key []byte, err error) {
	key = make([]byte, secretbox.KeySize)
	if _, err = cryptorand.Read(key); err != nil {
		return nil, nil, fmt.Errorf("generate trajectory data key: %w", err)
	}
	box, err := secretbox.New(key)
	if err != nil {
		return nil, nil, err
	}
	sealed, err = box.Seal(data)
	if err != nil {
		return nil, nil, err
	}
	if len(sealed) != len(data)+dshTrajectorySealOverhead {
		return nil, nil, errors.New("unexpected trajectory ciphertext overhead")
	}
	return sealed, key, nil
}

func openDSHTrajectory(sealed, key []byte, scheme string) ([]byte, error) {
	if scheme != dshTrajectoryEncryptionScheme || len(key) != secretbox.KeySize {
		return nil, errors.New("unsupported trajectory encryption metadata")
	}
	box, err := secretbox.New(key)
	if err != nil {
		return nil, err
	}
	return box.Open(sealed)
}

// requireUserTaskViewAccess is the shared authorization boundary for a task's
// transcript and DSH trajectory. Both artifacts can contain private prompts,
// tool arguments, and tool output, so the workspace and private-agent gates
// must remain identical.
func (h *Handler) requireUserTaskViewAccess(w http.ResponseWriter, r *http.Request, taskID string) (db.AgentTaskQueue, bool) {
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task_id")
	if !ok {
		return db.AgentTaskQueue{}, false
	}

	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return db.AgentTaskQueue{}, false
	}
	workspaceID := h.TaskService.ResolveTaskWorkspaceID(r.Context(), task)
	if workspaceID == "" || workspaceID != middleware.WorkspaceIDFromContext(r.Context()) {
		writeError(w, http.StatusNotFound, "task not found")
		return db.AgentTaskQueue{}, false
	}
	agent, err := h.Queries.GetAgent(r.Context(), task.AgentID)
	if err != nil || uuidToString(agent.WorkspaceID) != workspaceID {
		writeError(w, http.StatusNotFound, "task not found")
		return db.AgentTaskQueue{}, false
	}
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return db.AgentTaskQueue{}, false
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return db.AgentTaskQueue{}, false
	}
	return task, true
}

// UploadDSHTrajectory accepts the immutable native DSH JSONL ledger from the
// task-scoped agent process. The regular Auth middleware derives every actor
// coordinate below from the short-lived mat_ token; client-supplied identity
// headers cannot override them. It deliberately does not expose or proxy DSH's
// localhost web server, whose API also has mutation methods; Multica persists
// the read-only artifact and applies its own access controls when a user opens
// it.
func (h *Handler) UploadDSHTrajectory(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "DSH trajectory upload requires task-scoped authentication")
		return
	}
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task_id")
	if !ok {
		return
	}
	if r.Header.Get("X-Task-ID") != uuidToString(taskUUID) {
		writeError(w, http.StatusForbidden, "task token is not bound to this task")
		return
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	workspaceID := h.TaskService.ResolveTaskWorkspaceID(r.Context(), task)
	if workspaceID == "" || workspaceID != middleware.WorkspaceIDFromContext(r.Context()) ||
		r.Header.Get("X-Workspace-ID") != workspaceID ||
		r.Header.Get("X-Agent-ID") != uuidToString(task.AgentID) {
		writeError(w, http.StatusForbidden, "task token does not match the DSH task")
		return
	}
	runtime, err := h.Queries.GetAgentRuntime(r.Context(), task.RuntimeID)
	if err != nil || strings.TrimSpace(strings.ToLower(runtime.Provider)) != "dsh" {
		writeError(w, http.StatusForbidden, "task runtime is not a DSH runner")
		return
	}
	if h.Storage == nil {
		writeError(w, http.StatusServiceUnavailable, "trajectory storage is unavailable")
		return
	}
	if contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); contentType != "application/x-ndjson" {
		writeError(w, http.StatusUnsupportedMediaType, "content type must be application/x-ndjson")
		return
	}

	sessionID := strings.TrimSpace(r.Header.Get("X-DSH-Session-ID"))
	if !dshSessionIDPattern.MatchString(sessionID) {
		writeError(w, http.StatusBadRequest, "invalid DSH session id")
		return
	}
	expectedDigest := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Content-SHA256")))
	expectedDigestBytes, digestErr := hex.DecodeString(expectedDigest)
	if digestErr != nil || len(expectedDigestBytes) != sha256.Size || len(expectedDigest) != sha256.Size*2 {
		writeError(w, http.StatusBadRequest, "invalid trajectory SHA-256")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxDSHTrajectoryBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "DSH trajectory exceeds 32 MiB")
			return
		}
		writeError(w, http.StatusBadRequest, "failed to read DSH trajectory")
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "DSH trajectory is empty")
		return
	}
	actualDigestBytes := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(actualDigestBytes[:], expectedDigestBytes) != 1 {
		writeError(w, http.StatusBadRequest, "trajectory SHA-256 mismatch")
		return
	}
	header, eventCount, err := validateDSHTrajectory(data, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A managed employee Session may contain many tasks. Its immutable platform
	// binding, never an uploader-supplied header, authorizes the exported range.
	var boundSession, boundRequest string
	bindingErr := h.DB.QueryRow(r.Context(), `SELECT session_id,request_id::text FROM dsh_task_binding WHERE workspace_id=$1 AND agent_id=$2 AND task_id=$3`, workspaceID, task.AgentID, task.ID).Scan(&boundSession, &boundRequest)
	if bindingErr != nil && !errors.Is(bindingErr, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to inspect DSH task binding")
		return
	}
	if err := validateDSHUploadBinding(data, boundSession, boundRequest, bindingErr == nil); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	existing, existingErr := h.Queries.GetAgentTaskDSHTrajectory(r.Context(), taskUUID)
	if existingErr == nil {
		if existing.SessionID == sessionID && existing.Sha256 == expectedDigest {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusConflict, "a different DSH trajectory is already recorded for this task")
		return
	}
	if !errors.Is(existingErr, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to inspect DSH trajectory")
		return
	}

	sealed, encryptionKey, err := sealDSHTrajectory(data)
	if err != nil {
		slog.Error("encrypt DSH trajectory failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to protect DSH trajectory")
		return
	}
	sealedDigest := sha256.Sum256(sealed)
	storageKey := fmt.Sprintf(
		"task-dsh-trajectories/%s/%s/%s.enc",
		taskID,
		expectedDigest,
		hex.EncodeToString(sealedDigest[:]),
	)
	if _, err := h.Storage.Upload(r.Context(), storageKey, sealed, "application/octet-stream", ""); err != nil {
		slog.Error("upload DSH trajectory object failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to store DSH trajectory")
		return
	}
	persisted, err := h.persistDSHTrajectory(r.Context(), parseUUID(workspaceID), task, db.PutAgentTaskDSHTrajectoryParams{
		TaskID:           taskUUID,
		SessionID:        sessionID,
		StorageKey:       storageKey,
		EncryptionScheme: dshTrajectoryEncryptionScheme,
		EncryptionKey:    encryptionKey,
		Sha256:           expectedDigest,
		SizeBytes:        int64(len(data)),
		StoredSizeBytes:  int64(len(sealed)),
		EventCount:       eventCount,
		FormatVersion:    header.Version,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// A conflicting digest or deleted parent cannot reference this
			// attempt's distinct ciphertext key. This is a confirmed rollback.
			h.Storage.Delete(r.Context(), storageKey)
			writeError(w, http.StatusConflict, "the DSH task was removed or has a different trajectory")
			return
		}
		// Do not delete on an indeterminate database failure: a concurrent exact
		// retry may already reference this same content-addressed object. Leaving
		// an unindexed object is safer than breaking a committed trajectory.
		slog.Error("persist DSH trajectory metadata failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to index DSH trajectory")
		return
	}
	if persisted.StorageKey != storageKey {
		// An exact concurrent retry won first with its own data key and ciphertext.
		// The returned row remains authoritative; remove only this request's
		// distinct object so ciphertext and key can never be crossed.
		h.Storage.Delete(r.Context(), storageKey)
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetDSHTrajectory streams one native DSH JSONL ledger after applying the same
// workspace, A2A-principal, and private-agent gates as the task transcript.
func (h *Handler) GetDSHTrajectory(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	task, ok := h.requireUserTaskViewAccess(w, r, taskID)
	if !ok {
		return
	}
	trajectory, err := h.Queries.GetAgentTaskDSHTrajectory(r.Context(), task.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "DSH trajectory not found")
		return
	}
	if h.Storage == nil {
		writeError(w, http.StatusServiceUnavailable, "trajectory storage is unavailable")
		return
	}
	reader, err := h.Storage.GetReader(r.Context(), trajectory.StorageKey)
	if err != nil {
		slog.Error("read DSH trajectory object failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to read DSH trajectory")
		return
	}
	defer reader.Close()
	if trajectory.SizeBytes <= 0 || trajectory.SizeBytes > maxDSHTrajectoryBytes ||
		trajectory.StoredSizeBytes != trajectory.SizeBytes+dshTrajectorySealOverhead {
		slog.Error("DSH trajectory metadata size mismatch", "task_id", taskID)
		writeError(w, http.StatusInternalServerError, "DSH trajectory object is invalid")
		return
	}
	sealed, err := io.ReadAll(io.LimitReader(reader, maxDSHTrajectoryBytes+dshTrajectorySealOverhead+1))
	if err != nil || int64(len(sealed)) != trajectory.StoredSizeBytes {
		slog.Error("DSH trajectory object size mismatch", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "DSH trajectory object is invalid")
		return
	}
	data, err := openDSHTrajectory(sealed, trajectory.EncryptionKey, trajectory.EncryptionScheme)
	if err != nil || int64(len(data)) != trajectory.SizeBytes {
		slog.Error("decrypt DSH trajectory object failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "DSH trajectory object is invalid")
		return
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != trajectory.Sha256 {
		slog.Error("DSH trajectory object digest mismatch", "task_id", taskID)
		writeError(w, http.StatusInternalServerError, "DSH trajectory object is invalid")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-DSH-Session-ID", trajectory.SessionID)
	w.Header().Set("X-Content-SHA256", trajectory.Sha256)
	w.Header().Set("X-DSH-Event-Count", fmt.Sprintf("%d", trajectory.EventCount))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
