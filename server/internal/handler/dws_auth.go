package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type DWSAuthProfileResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	OwnerID     string `json:"owner_id,omitempty"`
	Label       string `json:"label"`
	CorpID      string `json:"corp_id,omitempty"`
	CorpName    string `json:"corp_name,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	UserName    string `json:"user_name,omitempty"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type DWSAuthSessionResponse struct {
	ID        string                  `json:"id"`
	Status    string                  `json:"status"`
	Message   string                  `json:"message,omitempty"`
	LoginURL  string                  `json:"login_url,omitempty"`
	UserCode  string                  `json:"user_code,omitempty"`
	Error     string                  `json:"error,omitempty"`
	Profile   *DWSAuthProfileResponse `json:"profile,omitempty"`
	CreatedAt string                  `json:"created_at"`
	UpdatedAt string                  `json:"updated_at"`
}

type dwsAuthSession struct {
	ID        string
	Status    string
	Message   string
	LoginURL  string
	UserCode  string
	Error     string
	Profile   *DWSAuthProfileResponse
	CreatedAt time.Time
	UpdatedAt time.Time
}

type DWSAuthSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*dwsAuthSession
}

func NewDWSAuthSessionStore() *DWSAuthSessionStore {
	return &DWSAuthSessionStore{sessions: make(map[string]*dwsAuthSession)}
}

func (s *DWSAuthSessionStore) Create(id string) *dwsAuthSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	session := &dwsAuthSession{
		ID:        id,
		Status:    "pending",
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.sessions[id] = session
	return cloneDWSAuthSession(session)
}

func (s *DWSAuthSessionStore) Get(id string) (*dwsAuthSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return nil, false
	}
	return cloneDWSAuthSession(session), true
}

func (s *DWSAuthSessionStore) Update(id string, mutate func(*dwsAuthSession)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return
	}
	mutate(session)
	session.UpdatedAt = time.Now()
}

func cloneDWSAuthSession(in *dwsAuthSession) *dwsAuthSession {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func (h *Handler) ListDWSAuthProfiles(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	rows, err := h.Queries.ListDWSAuthProfiles(r.Context(), wsUUID)
	if err != nil {
		writeError(w, 500, "failed to list DWS profiles")
		return
	}
	out := make([]DWSAuthProfileResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, dwsProfileToResponse(row))
	}
	writeJSON(w, 200, out)
}

func (h *Handler) BeginDWSAuth(w http.ResponseWriter, r *http.Request) {
	if h.DWSAuthBox == nil {
		writeError(w, 503, "DWS auth storage is not configured")
		return
	}
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	ownerUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	session := h.DWSAuthSessions.Create(randomID())
	go h.runDWSAuthSession(session.ID, wsUUID, ownerUUID)
	writeJSON(w, 202, dwsSessionToResponse(session))
}

func (h *Handler) GetDWSAuthStatus(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	sessionID := chi.URLParam(r, "sessionId")
	session, ok := h.DWSAuthSessions.Get(sessionID)
	if !ok {
		writeError(w, 404, "DWS auth session not found")
		return
	}
	writeJSON(w, 200, dwsSessionToResponse(session))
}

func (h *Handler) runDWSAuthSession(sessionID string, workspaceID, ownerID pgtype.UUID) {
	tmpDir, err := os.MkdirTemp("", "multica-dws-auth-*")
	if err != nil {
		h.failDWSAuthSession(sessionID, "failed to create DWS auth workspace")
		return
	}
	defer os.RemoveAll(tmpDir)

	homeDir := filepath.Join(tmpDir, "home")
	if err := os.MkdirAll(filepath.Join(homeDir, ".dws"), 0700); err != nil {
		h.failDWSAuthSession(sessionID, "failed to initialize DWS auth workspace")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.DWSAuthTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, h.cfg.DWSCLIPath, "auth", "login", "--device")
	cmd.Env = append(os.Environ(), dwsAuthEnv(homeDir)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		h.failDWSAuthSession(sessionID, "failed to start DWS login")
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		h.failDWSAuthSession(sessionID, "failed to start DWS login")
		return
	}
	if err := cmd.Start(); err != nil {
		h.failDWSAuthSession(sessionID, "failed to start DWS login: "+err.Error())
		return
	}
	done := make(chan struct{}, 2)
	go h.scanDWSAuthOutput(sessionID, stdout, done)
	go h.scanDWSAuthOutput(sessionID, stderr, done)

	err = cmd.Wait()
	<-done
	<-done
	if ctx.Err() != nil {
		h.failDWSAuthSession(sessionID, "DWS login timed out")
		return
	}
	if err != nil {
		h.failDWSAuthSession(sessionID, "DWS login failed: "+err.Error())
		return
	}

	commandCtx, commandCancel := context.WithTimeout(context.Background(), 30*time.Second)
	statusRaw, err := runDWSCommand(commandCtx, h.cfg.DWSCLIPath, homeDir, "auth", "status", "--format", "json")
	commandCancel()
	if err != nil {
		h.failDWSAuthSession(sessionID, "DWS login succeeded but status failed")
		return
	}
	archivePath := filepath.Join(tmpDir, "dws-auth.b64")
	commandCtx, commandCancel = context.WithTimeout(context.Background(), 30*time.Second)
	_, err = runDWSCommand(commandCtx, h.cfg.DWSCLIPath, homeDir, "auth", "export", "--base64", "-o", archivePath)
	commandCancel()
	if err != nil {
		h.failDWSAuthSession(sessionID, "DWS auth export failed")
		return
	}
	archiveRaw, err := os.ReadFile(archivePath)
	if err != nil {
		h.failDWSAuthSession(sessionID, "failed to read DWS auth export")
		return
	}
	archive := strings.TrimSpace(string(archiveRaw))
	if archive == "" {
		h.failDWSAuthSession(sessionID, "DWS auth export was empty")
		return
	}
	sealed, err := h.DWSAuthBox.Seal([]byte(archive))
	if err != nil {
		h.failDWSAuthSession(sessionID, "failed to encrypt DWS auth export")
		return
	}

	summary := summarizeDWSAuthStatus(statusRaw)
	label := summary.UserName
	if label == "" {
		label = summary.UserID
	}
	if summary.CorpName != "" {
		if label == "" {
			label = summary.CorpName
		} else {
			label = label + " @ " + summary.CorpName
		}
	}
	if label == "" {
		label = "DWS profile"
	}

	dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dbCancel()
	profile, err := h.Queries.CreateDWSAuthProfile(dbCtx, db.CreateDWSAuthProfileParams{
		WorkspaceID:          workspaceID,
		OwnerID:              ownerID,
		Label:                label,
		CorpID:               summary.CorpID,
		CorpName:             summary.CorpName,
		UserID:               summary.UserID,
		UserName:             summary.UserName,
		AuthArchiveEncrypted: sealed,
	})
	if err != nil {
		h.failDWSAuthSession(sessionID, "failed to save DWS auth profile")
		return
	}
	resp := dwsProfileToResponse(profile)
	h.DWSAuthSessions.Update(sessionID, func(s *dwsAuthSession) {
		s.Status = "succeeded"
		s.Message = "DWS profile connected"
		s.Profile = &resp
	})
}

func (h *Handler) scanDWSAuthOutput(sessionID string, r io.Reader, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		loginURL, userCode := parseDWSDeviceLine(line)
		h.DWSAuthSessions.Update(sessionID, func(s *dwsAuthSession) {
			s.Message = line
			if loginURL != "" {
				s.LoginURL = loginURL
			}
			if userCode != "" {
				s.UserCode = userCode
			}
		})
	}
}

func (h *Handler) failDWSAuthSession(sessionID, msg string) {
	slog.Warn("DWS auth session failed", "session_id", sessionID, "error", msg)
	h.DWSAuthSessions.Update(sessionID, func(s *dwsAuthSession) {
		s.Status = "failed"
		s.Error = msg
	})
}

func runDWSCommand(ctx context.Context, cliPath, homeDir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, cliPath, args...)
	cmd.Env = append(os.Environ(), dwsAuthEnv(homeDir)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func dwsAuthEnv(homeDir string) []string {
	return []string{
		"HOME=" + homeDir,
		"DWS_CONFIG_DIR=" + filepath.Join(homeDir, ".dws"),
		"XDG_DATA_HOME=" + filepath.Join(homeDir, ".local", "share"),
	}
}

var (
	dwsURLPattern  = regexp.MustCompile(`https?://[^\s'"<>]+`)
	dwsCodePattern = regexp.MustCompile(`(?i)(?:code|验证码|校验码|user code)[^A-Za-z0-9]*([A-Za-z0-9-]{4,})`)
)

func parseDWSDeviceLine(line string) (string, string) {
	loginURL := ""
	if match := dwsURLPattern.FindString(line); match != "" {
		loginURL = strings.TrimRight(match, ".,;)")
	}
	userCode := ""
	if match := dwsCodePattern.FindStringSubmatch(line); len(match) == 2 {
		userCode = match[1]
	}
	return loginURL, userCode
}

type dwsAuthSummary struct {
	CorpID   string
	CorpName string
	UserID   string
	UserName string
}

func summarizeDWSAuthStatus(raw string) dwsAuthSummary {
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return dwsAuthSummary{}
	}
	var summary dwsAuthSummary
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for key, value := range x {
				lower := strings.ToLower(key)
				str, _ := value.(string)
				switch lower {
				case "corpid", "corp_id", "orgid", "org_id":
					if summary.CorpID == "" {
						summary.CorpID = str
					}
				case "corpname", "corp_name", "orgname", "org_name":
					if summary.CorpName == "" {
						summary.CorpName = str
					}
				case "userid", "user_id", "staffid", "staff_id":
					if summary.UserID == "" {
						summary.UserID = str
					}
				case "username", "user_name", "nickname", "nick_name":
					if summary.UserName == "" {
						summary.UserName = str
					}
				}
				walk(value)
			}
		case []any:
			for _, item := range x {
				walk(item)
			}
		}
	}
	walk(payload)
	return summary
}

func dwsProfileToResponse(row db.DwsAuthProfile) DWSAuthProfileResponse {
	return DWSAuthProfileResponse{
		ID:          uuidToString(row.ID),
		WorkspaceID: uuidToString(row.WorkspaceID),
		OwnerID:     uuidToString(row.OwnerID),
		Label:       row.Label,
		CorpID:      row.CorpID,
		CorpName:    row.CorpName,
		UserID:      row.UserID,
		UserName:    row.UserName,
		Status:      row.Status,
		CreatedAt:   timestampToString(row.CreatedAt),
		UpdatedAt:   timestampToString(row.UpdatedAt),
	}
}

func dwsSessionToResponse(s *dwsAuthSession) DWSAuthSessionResponse {
	if s == nil {
		return DWSAuthSessionResponse{}
	}
	return DWSAuthSessionResponse{
		ID:        s.ID,
		Status:    s.Status,
		Message:   s.Message,
		LoginURL:  s.LoginURL,
		UserCode:  s.UserCode,
		Error:     s.Error,
		Profile:   s.Profile,
		CreatedAt: s.CreatedAt.Format(time.RFC3339),
		UpdatedAt: s.UpdatedAt.Format(time.RFC3339),
	}
}
