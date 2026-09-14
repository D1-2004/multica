package dshhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/google/uuid"
)

// FCBuildProvider intentionally cannot mount an employee Home or assume its
// role. Build sandboxes use only scoped object grants, without employee secrets.
type FCBuildProvider struct{ transport *FCProvider }

type FCBuildIdentity struct {
	WorkspaceID uuid.UUID
	BuildID     uuid.UUID
	Intent      uuid.UUID
	BuildKey    string
	TemplateID  string
}

var buildDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (b FCBuildIdentity) valid() bool {
	return b.WorkspaceID != uuid.Nil && b.BuildID != uuid.Nil && b.Intent != uuid.Nil &&
		buildDigestPattern.MatchString(b.BuildKey) && sandboxIDPattern.MatchString(b.TemplateID)
}

func (b FCBuildIdentity) labels() map[string]string {
	return map[string]string{
		"multica.dsh.build.intent":      b.Intent.String(),
		"multica.dsh.build.workspace":   b.WorkspaceID.String(),
		"multica.dsh.build.id":          b.BuildID.String(),
		"multica.dsh.build.key":         b.BuildKey,
		"multica.dsh.build.template-id": b.TemplateID,
	}
}

func (b FCBuildIdentity) matches(info sandboxInfo) bool {
	if !sandboxIDPattern.MatchString(info.ID) || info.Template == "" || len(info.Mounts) != 0 {
		return false
	}
	for key, value := range b.labels() {
		if info.Metadata[key] != value {
			return false
		}
	}
	return true
}

func NewFCBuildProvider(config FCConfig) (*FCBuildProvider, error) {
	if config.TimeoutSeconds < 60 || config.TimeoutSeconds > 3600 {
		return nil, errors.New("invalid FC build sandbox lifetime")
	}
	transport, err := newFCTransport(config)
	if err != nil {
		return nil, err
	}
	return &FCBuildProvider{transport: transport}, nil
}

func (p *FCBuildProvider) Scope() string { return p.transport.config.APIURL }

func (p *FCBuildProvider) Create(ctx context.Context, identity FCBuildIdentity) (string, error) {
	if !identity.valid() {
		return "", errors.New("invalid FC build create intent")
	}
	data, _, _, err := p.transport.request(ctx, http.MethodPost, "/sandboxes", map[string]any{
		"templateID": identity.TemplateID, "timeout": p.transport.config.TimeoutSeconds,
		"autoPause": false, "metadata": identity.labels(),
	})
	if err != nil {
		return "", err
	}
	var info sandboxInfo
	if err := json.Unmarshal(data, &info); err != nil || !sandboxIDPattern.MatchString(info.ID) {
		return "", errors.New("FC build create returned no valid sandbox ID")
	}
	return info.ID, nil
}

func (p *FCBuildProvider) FindCreated(ctx context.Context, identity FCBuildIdentity) (string, error) {
	if !identity.valid() {
		return "", errors.New("invalid FC build reconciliation identity")
	}
	return p.transport.findSandbox(ctx, "multica.dsh.build.intent", identity.Intent.String(), identity.matches)
}

// Inspect verifies the full durable identity before permitting exec or cleanup.
// The bool is false only for authoritative GET 404, never a transport timeout.
func (p *FCBuildProvider) Inspect(ctx context.Context, identity FCBuildIdentity, id string) (bool, string, error) {
	if !identity.valid() || !sandboxIDPattern.MatchString(id) {
		return false, "", errors.New("invalid FC build sandbox identity")
	}
	data, _, status, err := p.transport.request(ctx, http.MethodGet, "/sandboxes/"+id, nil)
	if status == http.StatusNotFound {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	var info sandboxInfo
	if err := json.Unmarshal(data, &info); err != nil || info.ID != id || !identity.matches(info) {
		return false, "", errors.New("FC build sandbox does not match the persisted intent")
	}
	return true, info.State, nil
}

func (p *FCBuildProvider) DestroyAndConfirmAbsent(ctx context.Context, identity FCBuildIdentity, id string) error {
	exists, _, err := p.Inspect(ctx, identity, id)
	if err != nil || !exists {
		return err
	}
	return p.transport.DestroyAndConfirmAbsent(ctx, id)
}
