package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/connectorconfig"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

const connectorAppBodyLimit = 256 << 10

// connectorAppView is the public shape of an OAuth application. The client
// secret and instance tokens are never copied onto it.
type connectorAppView struct {
	ID                    string                  `json:"id"`
	Provider              string                  `json:"provider"`
	DisplayName           string                  `json:"display_name"`
	ClientID              string                  `json:"client_id"`
	ClientSecretHint      string                  `json:"client_secret_hint"`
	ClientSecretSet       bool                    `json:"client_secret_set"`
	Scopes                string                  `json:"scopes"`
	AuthorizationEndpoint string                  `json:"authorization_endpoint"`
	TokenEndpoint         string                  `json:"token_endpoint"`
	CallbackMode          string                  `json:"callback_mode"`
	Enabled               bool                    `json:"enabled"`
	ActiveForCatalog      bool                    `json:"active_for_catalog"`
	AppIdentifier         string                  `json:"app_identifier"`
	InstallSlug           string                  `json:"install_slug"`
	PrivateKeySet         bool                    `json:"private_key_set"`
	PrivateKeyHint        string                  `json:"private_key_hint"`
	OptionalSecretSet     bool                    `json:"optional_secret_set"`
	OptionalSecretHint    string                  `json:"optional_secret_hint"`
	Instances             []connectorInstanceView `json:"instances"`
}

type connectorInstanceView struct {
	ID              string                 `json:"id"`
	Label           string                 `json:"label"`
	ExternalSubject string                 `json:"external_subject"`
	ExternalLogin   string                 `json:"external_login"`
	Status          string                 `json:"status"`
	Enabled         bool                   `json:"enabled"`
	TokenHint       string                 `json:"token_hint"`
	TokenSet        bool                   `json:"token_set"`
	Bindings        []connectorBindingView `json:"bindings"`
}

type connectorBindingView struct {
	ScopeKind string `json:"scope_kind"`
	ScopeID   string `json:"scope_id"`
}

type connectorAppWrite struct {
	Provider              *string `json:"provider"`
	DisplayName           *string `json:"display_name"`
	ClientID              *string `json:"client_id"`
	ClientSecret          *string `json:"client_secret"`
	ClearClientSecret     *bool   `json:"clear_client_secret"`
	Scopes                *string `json:"scopes"`
	AuthorizationEndpoint *string `json:"authorization_endpoint"`
	TokenEndpoint         *string `json:"token_endpoint"`
	CallbackMode          *string `json:"callback_mode"`
	Enabled               *bool   `json:"enabled"`
	PublicClient          *bool   `json:"public_client"`
	AppIdentifier         *string `json:"app_identifier"`
	InstallSlug           *string `json:"install_slug"`
	PrivateKey            *string `json:"private_key"`
	ClearPrivateKey       *bool   `json:"clear_private_key"`
	OptionalSecret        *string `json:"optional_secret"`
	ClearOptionalSecret   *bool   `json:"clear_optional_secret"`
}

type connectorInstanceWrite struct {
	Label           *string `json:"label"`
	ExternalSubject *string `json:"external_subject"`
	ExternalLogin   *string `json:"external_login"`
	Status          *string `json:"status"`
	Token           *string `json:"token"`
	ClearToken      *bool   `json:"clear_token"`
	Enabled         *bool   `json:"enabled"`
}

type connectorBindingsWrite struct {
	Bindings *[]connectorBindingView `json:"bindings"`
}

type connectorResolveWrite struct {
	Provider    string `json:"provider"`
	AgentID     string `json:"agent_id"`
	ProjectID   string `json:"project_id"`
	Environment string `json:"environment"`
}

type connectorResolveView struct {
	Matched       bool     `json:"matched"`
	Provider      string   `json:"provider"`
	AppID         string   `json:"app_id,omitempty"`
	InstanceID    string   `json:"instance_id,omitempty"`
	InstanceLabel string   `json:"instance_label,omitempty"`
	ScopeKind     string   `json:"scope_kind,omitempty"`
	ScopeID       string   `json:"scope_id,omitempty"`
	TokenReady    bool     `json:"token_ready"`
	Priority      []string `json:"priority"`
	Fallback      string   `json:"fallback,omitempty"`
}

func (h *Handler) ListConnectorApps(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.connectorAppWorkspace(w, r)
	if !ok {
		return
	}
	views, err := h.connectorAppViews(r.Context(), workspaceID)
	if err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"apps":         views,
		"priority":     connectorconfig.Priority,
		"catalog":      h.settingsCatalogViews(r.Context(), workspaceID),
		"callback_url": connectorcatalog.ProductionCallbackURL,
	})
}

func (h *Handler) CreateConnectorApp(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.connectorAppWorkspace(w, r)
	if !ok {
		return
	}
	var body connectorAppWrite
	if !decodeConnectorAppBody(w, r, &body) {
		return
	}
	in := connectorconfig.AppInput{
		Provider:              derefString(body.Provider),
		DisplayName:           derefString(body.DisplayName),
		ClientID:              derefString(body.ClientID),
		ClientSecret:          strings.TrimSpace(derefString(body.ClientSecret)),
		Scopes:                derefString(body.Scopes),
		AuthorizationEndpoint: derefString(body.AuthorizationEndpoint),
		TokenEndpoint:         derefString(body.TokenEndpoint),
		CallbackMode:          derefString(body.CallbackMode),
		Enabled:               body.Enabled,
		PublicClient:          body.PublicClient != nil && *body.PublicClient,
		AppIdentifier:         derefString(body.AppIdentifier),
		InstallSlug:           derefString(body.InstallSlug),
	}
	applySettingsPreset(&in)
	if err := h.sealConnectorAppExtras(&in, derefString(body.PrivateKey), derefString(body.OptionalSecret)); err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	ciphertext, hint, sealErr := h.sealConnectorAppSecret(in.ClientSecret)
	if sealErr != nil && in.ClientSecret != "" {
		writeConnectorConfigErrorStatus(w, sealErr)
		return
	}
	createdBy := requestUserID(r)
	app, err := connectorconfig.Create(r.Context(), h.DB, workspaceID, createdBy, in, ciphertext, hint)
	if err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	view, err := h.connectorAppView(r.Context(), workspaceID, app.ID)
	if err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (h *Handler) GetConnectorApp(w http.ResponseWriter, r *http.Request) {
	workspaceID, appID, ok := h.connectorAppIDs(w, r)
	if !ok {
		return
	}
	view, err := h.connectorAppView(r.Context(), workspaceID, appID)
	if err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) UpdateConnectorApp(w http.ResponseWriter, r *http.Request) {
	workspaceID, appID, ok := h.connectorAppIDs(w, r)
	if !ok {
		return
	}
	current, err := connectorconfig.Get(r.Context(), h.DB, workspaceID, appID)
	if err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	var body connectorAppWrite
	if !decodeConnectorAppBody(w, r, &body) {
		return
	}
	in := connectorconfig.AppInput{
		Provider:              firstSet(body.Provider, current.Provider),
		DisplayName:           firstSet(body.DisplayName, current.DisplayName),
		ClientID:              firstSet(body.ClientID, current.ClientID),
		Scopes:                firstSet(body.Scopes, current.Scopes),
		AuthorizationEndpoint: firstSet(body.AuthorizationEndpoint, current.AuthorizationEndpoint),
		TokenEndpoint:         firstSet(body.TokenEndpoint, current.TokenEndpoint),
		CallbackMode:          firstSet(body.CallbackMode, current.CallbackMode),
		Enabled:               body.Enabled,
		ClearSecret:           body.ClearClientSecret != nil && *body.ClearClientSecret,
		AppIdentifier:         firstSet(body.AppIdentifier, current.AppIdentifier),
		InstallSlug:           firstSet(body.InstallSlug, current.InstallSlug),
		ClearPrivate:          body.ClearPrivateKey != nil && *body.ClearPrivateKey,
		ClearOptional:         body.ClearOptionalSecret != nil && *body.ClearOptionalSecret,
	}
	applySettingsPreset(&in)
	if err := h.sealConnectorAppExtras(&in, derefString(body.PrivateKey), derefString(body.OptionalSecret)); err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	var ciphertext []byte
	var hint string
	rotate := false
	if secret := strings.TrimSpace(derefString(body.ClientSecret)); secret != "" && !in.ClearSecret {
		ciphertext, hint, err = h.sealConnectorAppSecret(secret)
		if err != nil {
			writeConnectorConfigErrorStatus(w, err)
			return
		}
		rotate = true
	}
	if _, err := connectorconfig.Update(r.Context(), h.DB, workspaceID, appID, in, ciphertext, hint, rotate); err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	view, err := h.connectorAppView(r.Context(), workspaceID, appID)
	if err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) DeleteConnectorApp(w http.ResponseWriter, r *http.Request) {
	workspaceID, appID, ok := h.connectorAppIDs(w, r)
	if !ok {
		return
	}
	if err := connectorconfig.Delete(r.Context(), h.DB, workspaceID, appID); err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) CreateConnectorAuthInstance(w http.ResponseWriter, r *http.Request) {
	workspaceID, appID, ok := h.connectorAppIDs(w, r)
	if !ok {
		return
	}
	var body connectorInstanceWrite
	if !decodeConnectorAppBody(w, r, &body) {
		return
	}
	token := strings.TrimSpace(derefString(body.Token))
	ciphertext, hint, err := h.sealConnectorInstanceToken(token)
	if err != nil && token != "" {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	in := connectorconfig.InstanceInput{
		Label:           derefString(body.Label),
		ExternalSubject: derefString(body.ExternalSubject),
		ExternalLogin:   derefString(body.ExternalLogin),
		Status:          derefString(body.Status),
		Token:           token,
		Enabled:         body.Enabled,
	}
	rec, err := connectorconfig.CreateInstance(r.Context(), h.DB, workspaceID, appID, requestUserID(r), in, ciphertext, hint)
	if err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, instanceView(rec))
}

func (h *Handler) UpdateConnectorAuthInstance(w http.ResponseWriter, r *http.Request) {
	workspaceID, appID, instanceID, ok := h.connectorInstanceIDs(w, r)
	if !ok {
		return
	}
	current, err := connectorconfig.GetInstance(r.Context(), h.DB, workspaceID, appID, instanceID)
	if err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	var body connectorInstanceWrite
	if !decodeConnectorAppBody(w, r, &body) {
		return
	}
	in := connectorconfig.InstanceInput{
		Label:           firstSet(body.Label, current.Label),
		ExternalSubject: firstSet(body.ExternalSubject, current.ExternalSubject),
		ExternalLogin:   firstSet(body.ExternalLogin, current.ExternalLogin),
		Status:          firstSet(body.Status, current.Status),
		Enabled:         body.Enabled,
		ClearToken:      body.ClearToken != nil && *body.ClearToken,
	}
	var ciphertext []byte
	var hint string
	rotate := false
	if token := strings.TrimSpace(derefString(body.Token)); token != "" && !in.ClearToken {
		ciphertext, hint, err = h.sealConnectorInstanceToken(token)
		if err != nil {
			writeConnectorConfigErrorStatus(w, err)
			return
		}
		rotate = true
		in.Token = token
	}
	rec, err := connectorconfig.UpdateInstance(r.Context(), h.DB, workspaceID, appID, instanceID, in, ciphertext, hint, rotate)
	if err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	writeJSON(w, http.StatusOK, instanceView(rec))
}

func (h *Handler) DeleteConnectorAuthInstance(w http.ResponseWriter, r *http.Request) {
	workspaceID, appID, instanceID, ok := h.connectorInstanceIDs(w, r)
	if !ok {
		return
	}
	if err := connectorconfig.DeleteInstance(r.Context(), h.DB, workspaceID, appID, instanceID); err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ReplaceConnectorAuthBindings(w http.ResponseWriter, r *http.Request) {
	workspaceID, appID, instanceID, ok := h.connectorInstanceIDs(w, r)
	if !ok {
		return
	}
	var body connectorBindingsWrite
	if !decodeConnectorAppBody(w, r, &body) {
		return
	}
	if body.Bindings == nil {
		writeError(w, http.StatusBadRequest, "invalid connector app")
		return
	}
	bindings := make([]connectorconfig.Binding, len(*body.Bindings))
	for i, binding := range *body.Bindings {
		bindings[i] = connectorconfig.Binding{ScopeKind: binding.ScopeKind, ScopeID: binding.ScopeID}
	}
	stored, err := connectorconfig.ReplaceBindings(r.Context(), h.DB, workspaceID, appID, instanceID, bindings)
	if err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	views := make([]connectorBindingView, len(stored))
	for i, binding := range stored {
		views[i] = connectorBindingView{ScopeKind: binding.ScopeKind, ScopeID: binding.ScopeID}
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": views})
}

func (h *Handler) ResolveConnectorApp(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.connectorAppWorkspace(w, r)
	if !ok {
		return
	}
	var body connectorResolveWrite
	if !decodeConnectorAppBody(w, r, &body) {
		return
	}
	provider, valid := connectorconfig.NormalizeProvider(body.Provider)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid connector app")
		return
	}
	view := connectorResolveView{Provider: provider, Priority: connectorconfig.Priority, Fallback: "legacy"}
	app, records, err := connectorconfig.InstancesForProvider(r.Context(), h.DB, workspaceID, provider)
	if errors.Is(err, connectorconfig.ErrNotFound) || errors.Is(err, connectorconfig.ErrSchemaMissing) {
		writeJSON(w, http.StatusOK, view)
		return
	}
	if err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	instances := make([]connectorconfig.Instance, len(records))
	byID := make(map[string]connectorconfig.InstanceRecord, len(records))
	for i, rec := range records {
		instances[i] = rec.Instance
		byID[rec.ID] = rec
	}
	picked, rank, matched := connectorconfig.Select(instances, connectorconfig.CallContext{
		AgentID: body.AgentID, ProjectID: body.ProjectID, Environment: body.Environment,
	})
	if !matched {
		writeJSON(w, http.StatusOK, view)
		return
	}
	rec := byID[picked.ID]
	secret, openErr := connectorconfig.OpenToken(h.InternalConnectorSecretBox, rec.TokenCiphertext)
	ready := tokenReady(secret, openErr, time.Now())
	var scopeID string
	for _, binding := range picked.Bindings {
		if binding.ScopeKind == rank {
			scopeID = binding.ScopeID
			break
		}
	}
	writeJSON(w, http.StatusOK, connectorResolveView{
		Matched: true, Provider: provider, AppID: app.ID, InstanceID: picked.ID, InstanceLabel: picked.Label,
		ScopeKind: rank, ScopeID: scopeID, TokenReady: ready, Priority: connectorconfig.Priority,
	})
}

func (h *Handler) connectorAppWorkspace(w http.ResponseWriter, r *http.Request) (string, bool) {
	workspaceID := workspaceIDFromURL(r, "id")
	if _, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id"); !ok {
		return "", false
	}
	if r.Header.Get("X-Actor-Source") == "workspace_mcp_token" {
		bound := strings.TrimSpace(r.Header.Get("X-Workspace-ID"))
		if bound == "" || bound != workspaceID {
			writeError(w, http.StatusForbidden, "workspace_mcp_workspace_mismatch")
			return "", false
		}
	}
	return workspaceID, true
}

func (h *Handler) connectorAppIDs(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	workspaceID, ok := h.connectorAppWorkspace(w, r)
	if !ok {
		return "", "", false
	}
	appID := strings.TrimSpace(chi.URLParam(r, "appId"))
	if _, ok := parseUUIDOrBadRequest(w, appID, "app_id"); !ok {
		return "", "", false
	}
	return workspaceID, appID, true
}

func (h *Handler) connectorInstanceIDs(w http.ResponseWriter, r *http.Request) (string, string, string, bool) {
	workspaceID, appID, ok := h.connectorAppIDs(w, r)
	if !ok {
		return "", "", "", false
	}
	instanceID := strings.TrimSpace(chi.URLParam(r, "instanceId"))
	if _, ok := parseUUIDOrBadRequest(w, instanceID, "instance_id"); !ok {
		return "", "", "", false
	}
	return workspaceID, appID, instanceID, true
}

func (h *Handler) connectorAppViews(ctx context.Context, workspaceID string) ([]connectorAppView, error) {
	apps, err := connectorconfig.List(ctx, h.DB, workspaceID)
	if err != nil {
		return nil, err
	}
	active := map[string]bool{}
	views := make([]connectorAppView, 0, len(apps))
	for _, app := range apps {
		view := appView(app, false)
		if app.Enabled && !active[app.Provider] {
			active[app.Provider] = true
			view.ActiveForCatalog = true
		}
		views = append(views, view)
	}
	return views, nil
}

func (h *Handler) connectorAppView(ctx context.Context, workspaceID, appID string) (connectorAppView, error) {
	views, err := h.connectorAppViews(ctx, workspaceID)
	if err != nil {
		return connectorAppView{}, err
	}
	for _, view := range views {
		if view.ID == appID {
			return view, nil
		}
	}
	return connectorAppView{}, connectorconfig.ErrNotFound
}

func (h *Handler) sealConnectorAppSecret(secret string) ([]byte, string, error) {
	if secret == "" {
		return nil, "", nil
	}
	sealed, err := connectorconfig.SealString(h.InternalConnectorSecretBox, secret)
	if err != nil {
		return nil, "", err
	}
	return sealed, connectorconfig.Hint(secret), nil
}

func (h *Handler) sealConnectorInstanceToken(token string) ([]byte, string, error) {
	if token == "" {
		return nil, "", nil
	}
	sealed, err := connectorconfig.SealToken(h.InternalConnectorSecretBox, token)
	if err != nil {
		return nil, "", err
	}
	return sealed, connectorconfig.Hint(token), nil
}

func appView(app connectorconfig.App, active bool) connectorAppView {
	instances := make([]connectorInstanceView, 0, len(app.Instances))
	for _, rec := range app.Instances {
		instances = append(instances, instanceView(rec))
	}
	return connectorAppView{
		ID: app.ID, Provider: app.Provider, DisplayName: app.DisplayName, ClientID: app.ClientID,
		ClientSecretHint: app.SecretHint, ClientSecretSet: len(app.SecretCiphertext) > 0,
		Scopes: app.Scopes, AuthorizationEndpoint: app.AuthorizationEndpoint, TokenEndpoint: app.TokenEndpoint,
		CallbackMode: app.CallbackMode, Enabled: app.Enabled, ActiveForCatalog: active, Instances: instances,
		AppIdentifier: app.AppIdentifier, InstallSlug: app.InstallSlug,
		PrivateKeySet: len(app.PrivateKeyCiphertext) > 0, PrivateKeyHint: app.PrivateKeyHint,
		OptionalSecretSet: len(app.OptionalCiphertext) > 0, OptionalSecretHint: app.OptionalHint,
	}
}

func applySettingsPreset(in *connectorconfig.AppInput) {
	spec, ok := connectorcatalog.SettingsSpecFor(strings.ToLower(strings.TrimSpace(in.Provider)))
	if !ok || spec.Mode != "preregistered" {
		return
	}
	if spec.AuthorizationEndpoint != "" {
		in.AuthorizationEndpoint = spec.AuthorizationEndpoint
	}
	if spec.TokenEndpoint != "" {
		in.TokenEndpoint = spec.TokenEndpoint
	}
	in.Scopes = spec.Scopes
	if strings.TrimSpace(in.DisplayName) == "" {
		in.DisplayName = spec.Name
	}
	if strings.TrimSpace(in.CallbackMode) == "" {
		in.CallbackMode = connectorconfig.CallbackProductionForward
	}
}

func (h *Handler) sealConnectorAppExtras(in *connectorconfig.AppInput, privateKey, optional string) error {
	privateKey = strings.TrimSpace(privateKey)
	if privateKey != "" {
		if !strings.Contains(privateKey, "PRIVATE KEY") || !strings.Contains(privateKey, "-----BEGIN") {
			return connectorconfig.ErrInvalid
		}
		sealed, err := connectorconfig.SealBytes(h.InternalConnectorSecretBox, privateKey)
		if err != nil {
			return err
		}
		in.PrivateCipher, in.PrivateHint, in.RotatePrivate = sealed, connectorconfig.Hint(privateKey), true
	}
	optional = strings.TrimSpace(optional)
	if optional != "" {
		sealed, err := connectorconfig.SealString(h.InternalConnectorSecretBox, optional)
		if err != nil {
			return err
		}
		in.OptionalCipher, in.OptionalHint, in.RotateOptional = sealed, connectorconfig.Hint(optional), true
	}
	return nil
}

type settingsCatalogView struct {
	connectorcatalog.SettingsSpec
	EnvConfigured bool   `json:"env_configured"`
	ConnectorID   string `json:"connector_id,omitempty"`
}

func (h *Handler) settingsCatalogViews(ctx context.Context, workspaceID string) []settingsCatalogView {
	added := map[string]string{}
	rows, err := h.DB.Query(ctx, `SELECT catalog_slug, id::text FROM internal_connector
		WHERE workspace_id = $1::uuid AND catalog_slug <> ''
		ORDER BY created_at ASC`, workspaceID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var slug, id string
			if scanErr := rows.Scan(&slug, &id); scanErr == nil {
				if _, exists := added[slug]; !exists {
					added[slug] = id
				}
			}
		}
	}
	specs := connectorcatalog.SettingsCatalog()
	out := make([]settingsCatalogView, 0, len(specs))
	for _, spec := range specs {
		out = append(out, settingsCatalogView{
			SettingsSpec:  spec,
			EnvConfigured: connectorAppEnvConfigured(spec.Slug),
			ConnectorID:   added[spec.Slug],
		})
	}
	return out
}

func instanceView(rec connectorconfig.InstanceRecord) connectorInstanceView {
	bindings := make([]connectorBindingView, 0, len(rec.Bindings))
	for _, binding := range rec.Bindings {
		bindings = append(bindings, connectorBindingView{ScopeKind: binding.ScopeKind, ScopeID: binding.ScopeID})
	}
	return connectorInstanceView{
		ID: rec.ID, Label: rec.Label, ExternalSubject: rec.ExternalSubject, ExternalLogin: rec.ExternalLogin,
		Status: rec.Status, Enabled: rec.Enabled, TokenHint: rec.TokenHint, TokenSet: len(rec.TokenCiphertext) > 0,
		Bindings: bindings,
	}
}

func decodeConnectorAppBody(w http.ResponseWriter, r *http.Request, dest any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, connectorAppBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		writeError(w, http.StatusBadRequest, "invalid connector app")
		return false
	}
	return true
}

func writeConnectorConfigErrorStatus(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, connectorconfig.ErrNotFound):
		writeError(w, http.StatusNotFound, "connector app not found")
	case errors.Is(err, connectorconfig.ErrConflict):
		writeError(w, http.StatusConflict, "connector app already exists")
	case errors.Is(err, connectorconfig.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid connector app")
	case errors.Is(err, connectorconfig.ErrSchemaMissing):
		writeError(w, http.StatusServiceUnavailable, "connector app storage is not ready")
	case errors.Is(err, connectorconfig.ErrSecretUnavailable):
		writeError(w, http.StatusServiceUnavailable, "connector credential storage is not configured")
	default:
		slog.Error("connector app request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "connector app request failed")
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func firstSet(value *string, current string) string {
	if value == nil {
		return current
	}
	return *value
}

// tokenReady is referenced so a future caller can share the usable-token
// check with the task resolver without opening the secret twice.
func tokenReady(secret contextcap.Secret, err error, now time.Time) bool {
	return err == nil && secret.Usable(now)
}
