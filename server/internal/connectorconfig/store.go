package connectorconfig

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound      = errors.New("connector app not found")
	ErrConflict      = errors.New("connector app conflict")
	ErrSchemaMissing = errors.New("connector app schema is not migrated")
	ErrInvalid       = errors.New("invalid connector app")
)

// DB is the subset of a pgx pool the store uses.
type DB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// App is one OAuth application row. SecretCiphertext is never serialized.
type App struct {
	ID                    string
	WorkspaceID           string
	Provider              string
	DisplayName           string
	ClientID              string
	SecretCiphertext      []byte
	SecretHint            string
	Scopes                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	CallbackMode          string
	Enabled               bool
	Instances             []InstanceRecord
}

// InstanceRecord is an authorization instance plus its sealed token.
type InstanceRecord struct {
	Instance
	TokenCiphertext []byte
	TokenHint       string
}

// AppInput is a create or update. Nil pointers on update mean unchanged.
type AppInput struct {
	Provider              string
	DisplayName           string
	ClientID              string
	ClientSecret          string
	ClearSecret           bool
	Scopes                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	CallbackMode          string
	Enabled               *bool
	PublicClient          bool
}

// InstanceInput is a create or update of an authorization instance.
type InstanceInput struct {
	Label           string
	ExternalSubject string
	ExternalLogin   string
	Status          string
	Token           string
	ClearToken      bool
	Enabled         *bool
}

func mapDB(err error) error {
	if err == nil || errors.Is(err, pgx.ErrNoRows) {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "42P01":
			return ErrSchemaMissing
		case "23505":
			return ErrConflict
		case "23514":
			return ErrInvalid
		}
	}
	return err
}

const appColumns = `id::text, workspace_id::text, provider, display_name, client_id,
	client_secret_ciphertext, client_secret_hint, scopes, authorization_endpoint, token_endpoint,
	callback_mode, enabled`

func scanApp(row pgx.Row) (App, error) {
	var app App
	err := row.Scan(&app.ID, &app.WorkspaceID, &app.Provider, &app.DisplayName, &app.ClientID,
		&app.SecretCiphertext, &app.SecretHint, &app.Scopes, &app.AuthorizationEndpoint, &app.TokenEndpoint,
		&app.CallbackMode, &app.Enabled)
	return app, mapDB(err)
}

// List returns the workspace's applications with instances and bindings.
// Ciphertext stays on the struct for the caller that needs to open it and
// must not be written to a response.
func List(ctx context.Context, db DB, workspaceID string) ([]App, error) {
	rows, err := db.Query(ctx, `SELECT `+appColumns+` FROM connector_app
		WHERE workspace_id = $1::uuid ORDER BY provider, created_at, id`, workspaceID)
	if err != nil {
		return nil, mapDB(err)
	}
	defer rows.Close()
	apps := []App{}
	index := map[string]int{}
	for rows.Next() {
		app, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		index[app.ID] = len(apps)
		apps = append(apps, app)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDB(err)
	}
	if len(apps) == 0 {
		return apps, nil
	}
	if err := attachInstances(ctx, db, workspaceID, apps, index); err != nil {
		return nil, err
	}
	return apps, nil
}

func attachInstances(ctx context.Context, db DB, workspaceID string, apps []App, index map[string]int) error {
	rows, err := db.Query(ctx, `SELECT id::text, app_id::text, label, external_subject, external_login, status,
		token_ciphertext, token_hint, enabled
		FROM connector_auth_instance WHERE workspace_id = $1::uuid ORDER BY created_at, id`, workspaceID)
	if err != nil {
		return mapDB(err)
	}
	defer rows.Close()
	instIndex := map[string][2]int{}
	for rows.Next() {
		var rec InstanceRecord
		if err := rows.Scan(&rec.ID, &rec.AppID, &rec.Label, &rec.ExternalSubject, &rec.ExternalLogin, &rec.Status,
			&rec.TokenCiphertext, &rec.TokenHint, &rec.Enabled); err != nil {
			return mapDB(err)
		}
		pos, ok := index[rec.AppID]
		if !ok {
			continue
		}
		apps[pos].Instances = append(apps[pos].Instances, rec)
		instIndex[rec.ID] = [2]int{pos, len(apps[pos].Instances) - 1}
	}
	if err := rows.Err(); err != nil {
		return mapDB(err)
	}
	brows, err := db.Query(ctx, `SELECT instance_id::text, scope_kind, scope_id
		FROM connector_auth_binding WHERE workspace_id = $1::uuid ORDER BY scope_kind, scope_id`, workspaceID)
	if err != nil {
		return mapDB(err)
	}
	defer brows.Close()
	for brows.Next() {
		var instanceID, kind, scopeID string
		if err := brows.Scan(&instanceID, &kind, &scopeID); err != nil {
			return mapDB(err)
		}
		loc, ok := instIndex[instanceID]
		if !ok {
			continue
		}
		apps[loc[0]].Instances[loc[1]].Bindings = append(apps[loc[0]].Instances[loc[1]].Bindings, Binding{ScopeKind: kind, ScopeID: scopeID})
	}
	return mapDB(brows.Err())
}

// Create inserts an application. secretCiphertext may be nil for a public client.
func Create(ctx context.Context, db DB, workspaceID, createdBy string, in AppInput, secretCiphertext []byte, hint string) (App, error) {
	provider, callback, err := validateAppInput(in, true)
	if err != nil {
		return App{}, err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	row := db.QueryRow(ctx, `INSERT INTO connector_app
		(workspace_id, provider, display_name, client_id, client_secret_ciphertext, client_secret_hint,
		 scopes, authorization_endpoint, token_endpoint, callback_mode, enabled, created_by)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULLIF($12, '')::uuid)
		RETURNING `+appColumns,
		workspaceID, provider, strings.TrimSpace(in.DisplayName), strings.TrimSpace(in.ClientID), secretCiphertext, hint,
		strings.TrimSpace(in.Scopes), strings.TrimSpace(in.AuthorizationEndpoint), strings.TrimSpace(in.TokenEndpoint),
		callback, enabled, createdBy)
	return scanApp(row)
}

// Update patches an application. An empty client secret keeps the stored one.
func Update(ctx context.Context, db DB, workspaceID, appID string, in AppInput, secretCiphertext []byte, hint string, rotateSecret bool) (App, error) {
	current, err := Get(ctx, db, workspaceID, appID)
	if err != nil {
		return App{}, err
	}
	provider := current.Provider
	if strings.TrimSpace(in.Provider) != "" {
		var ok bool
		provider, ok = NormalizeProvider(in.Provider)
		if !ok {
			return App{}, ErrInvalid
		}
	}
	display := current.DisplayName
	if in.DisplayName != "" || in.Provider != "" {
		display = strings.TrimSpace(in.DisplayName)
		if in.DisplayName == "" {
			display = current.DisplayName
		}
	}
	clientID := current.ClientID
	if strings.TrimSpace(in.ClientID) != "" {
		clientID = strings.TrimSpace(in.ClientID)
	}
	scopes := current.Scopes
	if in.Scopes != "" || in.ClientID != "" {
		scopes = strings.TrimSpace(in.Scopes)
		if in.Scopes == "" && in.ClientID == "" {
			scopes = current.Scopes
		}
	}
	// Pointer-free text fields: a request that omits them arrives as empty.
	// The handler sets a sentinel by passing the current value when omitted.
	authz := strings.TrimSpace(in.AuthorizationEndpoint)
	tokenURL := strings.TrimSpace(in.TokenEndpoint)
	callback := current.CallbackMode
	if strings.TrimSpace(in.CallbackMode) != "" {
		var ok bool
		callback, ok = NormalizeCallbackMode(in.CallbackMode)
		if !ok {
			return App{}, ErrInvalid
		}
	}
	enabled := current.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	ciphertext := current.SecretCiphertext
	secretHint := current.SecretHint
	if in.ClearSecret {
		ciphertext, secretHint = nil, ""
	} else if rotateSecret {
		ciphertext, secretHint = secretCiphertext, hint
	}
	if len(clientID) < 1 || len(clientID) > 512 || len(display) > 120 || len(scopes) > 2048 || !validOAuthEndpoint(authz) || !validOAuthEndpoint(tokenURL) {
		return App{}, ErrInvalid
	}
	row := db.QueryRow(ctx, `UPDATE connector_app SET
		provider = $3, display_name = $4, client_id = $5, client_secret_ciphertext = $6, client_secret_hint = $7,
		scopes = $8, authorization_endpoint = $9, token_endpoint = $10, callback_mode = $11, enabled = $12, updated_at = now()
		WHERE workspace_id = $1::uuid AND id = $2::uuid
		RETURNING `+appColumns,
		workspaceID, appID, provider, display, clientID, ciphertext, secretHint, scopes, authz, tokenURL, callback, enabled)
	return scanApp(row)
}

// Get loads one application without instances.
func Get(ctx context.Context, db DB, workspaceID, appID string) (App, error) {
	return scanApp(db.QueryRow(ctx, `SELECT `+appColumns+` FROM connector_app WHERE workspace_id = $1::uuid AND id = $2::uuid`, workspaceID, appID))
}

// Delete removes an application and, by cascade, its instances and bindings.
func Delete(ctx context.Context, db DB, workspaceID, appID string) error {
	tag, err := db.Exec(ctx, `DELETE FROM connector_app WHERE workspace_id = $1::uuid AND id = $2::uuid`, workspaceID, appID)
	if err != nil {
		return mapDB(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func validateAppInput(in AppInput, create bool) (provider, callback string, err error) {
	var ok bool
	provider, ok = NormalizeProvider(in.Provider)
	if !ok {
		return "", "", ErrInvalid
	}
	clientID := strings.TrimSpace(in.ClientID)
	if len(clientID) < 1 || len(clientID) > 512 || len(strings.TrimSpace(in.DisplayName)) > 120 {
		return "", "", ErrInvalid
	}
	if len(strings.TrimSpace(in.Scopes)) > 2048 || !validOAuthEndpoint(in.AuthorizationEndpoint) || !validOAuthEndpoint(in.TokenEndpoint) {
		return "", "", ErrInvalid
	}
	callback, ok = NormalizeCallbackMode(in.CallbackMode)
	if !ok {
		return "", "", ErrInvalid
	}
	if create && strings.TrimSpace(in.ClientSecret) == "" && !in.PublicClient {
		return "", "", ErrInvalid
	}
	return provider, callback, nil
}

// OldestEnabled returns the catalog client for provider: the oldest enabled
// application. ErrNotFound when the workspace has none.
func OldestEnabled(ctx context.Context, db DB, workspaceID, provider string) (App, error) {
	provider, ok := NormalizeProvider(provider)
	if !ok {
		return App{}, ErrInvalid
	}
	return scanApp(db.QueryRow(ctx, `SELECT `+appColumns+` FROM connector_app
		WHERE workspace_id = $1::uuid AND provider = $2 AND enabled
		ORDER BY created_at, id LIMIT 1`, workspaceID, provider))
}

// CreateInstance inserts an authorization instance.
func CreateInstance(ctx context.Context, db DB, workspaceID, appID, createdBy string, in InstanceInput, tokenCiphertext []byte, hint string) (InstanceRecord, error) {
	if _, err := Get(ctx, db, workspaceID, appID); err != nil {
		return InstanceRecord{}, err
	}
	label, status, err := validateInstance(in, true)
	if err != nil {
		return InstanceRecord{}, err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	var rec InstanceRecord
	err = db.QueryRow(ctx, `INSERT INTO connector_auth_instance
		(app_id, workspace_id, label, external_subject, external_login, status, token_ciphertext, token_hint, enabled, created_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, '')::uuid)
		RETURNING id::text, app_id::text, label, external_subject, external_login, status, token_ciphertext, token_hint, enabled`,
		appID, workspaceID, label, strings.TrimSpace(in.ExternalSubject), strings.TrimSpace(in.ExternalLogin), status,
		tokenCiphertext, hint, enabled, createdBy).Scan(
		&rec.ID, &rec.AppID, &rec.Label, &rec.ExternalSubject, &rec.ExternalLogin, &rec.Status, &rec.TokenCiphertext, &rec.TokenHint, &rec.Enabled)
	return rec, mapDB(err)
}

// UpdateInstance patches an authorization instance.
func UpdateInstance(ctx context.Context, db DB, workspaceID, appID, instanceID string, in InstanceInput, tokenCiphertext []byte, hint string, rotate bool) (InstanceRecord, error) {
	current, err := GetInstance(ctx, db, workspaceID, appID, instanceID)
	if err != nil {
		return InstanceRecord{}, err
	}
	label := current.Label
	if strings.TrimSpace(in.Label) != "" {
		label = strings.TrimSpace(in.Label)
	}
	subject := current.ExternalSubject
	login := current.ExternalLogin
	if in.ExternalSubject != "" || in.Label != "" {
		subject = strings.TrimSpace(in.ExternalSubject)
		if in.ExternalSubject == "" && in.Label == "" {
			subject = current.ExternalSubject
		}
	}
	if in.ExternalLogin != "" || in.Label != "" {
		login = strings.TrimSpace(in.ExternalLogin)
		if in.ExternalLogin == "" && in.Label == "" {
			login = current.ExternalLogin
		}
	}
	status := current.Status
	if strings.TrimSpace(in.Status) != "" {
		status = strings.TrimSpace(in.Status)
	}
	enabled := current.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	ciphertext, tokenHint := current.TokenCiphertext, current.TokenHint
	if in.ClearToken {
		ciphertext, tokenHint = nil, ""
	} else if rotate {
		ciphertext, tokenHint = tokenCiphertext, hint
		if status == StatusPending {
			status = StatusActive
		}
	}
	if len(label) < 1 || len(label) > 120 || !validStatus(status) || len(subject) > 256 || len(login) > 128 {
		return InstanceRecord{}, ErrInvalid
	}
	var rec InstanceRecord
	err = db.QueryRow(ctx, `UPDATE connector_auth_instance SET
		label = $4, external_subject = $5, external_login = $6, status = $7, token_ciphertext = $8, token_hint = $9,
		enabled = $10, updated_at = now()
		WHERE workspace_id = $1::uuid AND app_id = $2::uuid AND id = $3::uuid
		RETURNING id::text, app_id::text, label, external_subject, external_login, status, token_ciphertext, token_hint, enabled`,
		workspaceID, appID, instanceID, label, subject, login, status, ciphertext, tokenHint, enabled).Scan(
		&rec.ID, &rec.AppID, &rec.Label, &rec.ExternalSubject, &rec.ExternalLogin, &rec.Status, &rec.TokenCiphertext, &rec.TokenHint, &rec.Enabled)
	return rec, mapDB(err)
}

// GetInstance loads one authorization instance without its bindings.
func GetInstance(ctx context.Context, db DB, workspaceID, appID, instanceID string) (InstanceRecord, error) {
	var rec InstanceRecord
	err := db.QueryRow(ctx, `SELECT id::text, app_id::text, label, external_subject, external_login, status,
		token_ciphertext, token_hint, enabled
		FROM connector_auth_instance WHERE workspace_id = $1::uuid AND app_id = $2::uuid AND id = $3::uuid`,
		workspaceID, appID, instanceID).Scan(&rec.ID, &rec.AppID, &rec.Label, &rec.ExternalSubject, &rec.ExternalLogin,
		&rec.Status, &rec.TokenCiphertext, &rec.TokenHint, &rec.Enabled)
	return rec, mapDB(err)
}

// DeleteInstance removes an instance and its bindings.
func DeleteInstance(ctx context.Context, db DB, workspaceID, appID, instanceID string) error {
	tag, err := db.Exec(ctx, `DELETE FROM connector_auth_instance
		WHERE workspace_id = $1::uuid AND app_id = $2::uuid AND id = $3::uuid`, workspaceID, appID, instanceID)
	if err != nil {
		return mapDB(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ReplaceBindings replaces the instance's scope bindings in one statement,
// so a conflict leaves the previous bindings in place. The returned
// bindings are the normalized set that was stored.
func ReplaceBindings(ctx context.Context, db DB, workspaceID, appID, instanceID string, bindings []Binding) ([]Binding, error) {
	if _, err := GetInstance(ctx, db, workspaceID, appID, instanceID); err != nil {
		return nil, err
	}
	normalized, err := normalizeBindings(bindings)
	if err != nil {
		return nil, err
	}
	kinds := make([]string, len(normalized))
	ids := make([]string, len(normalized))
	for i, binding := range normalized {
		kinds[i], ids[i] = binding.ScopeKind, binding.ScopeID
	}
	_, err = db.Exec(ctx, `WITH cleared AS (
		DELETE FROM connector_auth_binding
		WHERE workspace_id = $1::uuid AND instance_id = $2::uuid
	)
	INSERT INTO connector_auth_binding (instance_id, app_id, workspace_id, scope_kind, scope_id)
	SELECT $2::uuid, $3::uuid, $1::uuid, kind, scope_id
	FROM unnest($4::text[], $5::text[]) AS incoming(kind, scope_id)`,
		workspaceID, instanceID, appID, kinds, ids)
	if err != nil {
		return nil, mapDB(err)
	}
	return normalized, nil
}

func validOAuthEndpoint(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	if len(raw) > 512 {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return false
	}
	return parsed.Scheme == "https" || parsed.Scheme == "http"
}

func normalizeBindings(bindings []Binding) ([]Binding, error) {
	if len(bindings) > 32 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	out := make([]Binding, 0, len(bindings))
	for _, binding := range bindings {
		kind := strings.TrimSpace(binding.ScopeKind)
		id := strings.TrimSpace(binding.ScopeID)
		switch kind {
		case ScopeWorkspace:
			id = ""
		case ScopeAgent, ScopeProject:
			if len(id) < 1 || len(id) > 128 || strings.ContainsAny(id, " \r\n\x00") {
				return nil, ErrInvalid
			}
		case ScopeEnvironment:
			id = NormalizeEnvironment(id)
			if len(id) < 1 || len(id) > 128 || strings.ContainsAny(id, " \r\n\x00") {
				return nil, ErrInvalid
			}
		default:
			return nil, ErrInvalid
		}
		key := kind + "\x00" + id
		if seen[key] {
			return nil, ErrInvalid
		}
		seen[key] = true
		out = append(out, Binding{ScopeKind: kind, ScopeID: id})
	}
	return out, nil
}

func validateInstance(in InstanceInput, create bool) (label, status string, err error) {
	label = strings.TrimSpace(in.Label)
	if len(label) < 1 || len(label) > 120 || len(strings.TrimSpace(in.ExternalSubject)) > 256 || len(strings.TrimSpace(in.ExternalLogin)) > 128 {
		return "", "", ErrInvalid
	}
	status = strings.TrimSpace(in.Status)
	if status == "" {
		if create && strings.TrimSpace(in.Token) != "" {
			status = StatusActive
		} else if create {
			status = StatusPending
		}
	}
	if !validStatus(status) {
		return "", "", ErrInvalid
	}
	return label, status, nil
}

func validStatus(status string) bool {
	switch status {
	case StatusPending, StatusActive, StatusDisabled, StatusNeedsReauth:
		return true
	default:
		return false
	}
}

// InstancesForProvider loads enabled applications' instances for selection.
// The oldest enabled application is the catalog client; its instances are returned.
func InstancesForProvider(ctx context.Context, db DB, workspaceID, provider string) (App, []InstanceRecord, error) {
	app, err := OldestEnabled(ctx, db, workspaceID, provider)
	if err != nil {
		return App{}, nil, err
	}
	apps := []App{app}
	if err := attachInstances(ctx, db, workspaceID, apps, map[string]int{app.ID: 0}); err != nil {
		return App{}, nil, err
	}
	return apps[0], apps[0].Instances, nil
}
