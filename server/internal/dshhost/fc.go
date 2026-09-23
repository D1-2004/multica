package dshhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const MountPath = "/mnt/multica"

// DefaultSandboxTaskTimeoutSeconds is the employee-host create/renew floor.
// Config may raise it; leftover 3600 cannot restore the old one-hour wall.
const DefaultSandboxTaskTimeoutSeconds = 4800

func SandboxTaskTimeoutSeconds(configured int) int {
	if configured > DefaultSandboxTaskTimeoutSeconds {
		return configured
	}
	return DefaultSandboxTaskTimeoutSeconds
}

var sandboxIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

var (
	destroyAbsenceWait = 800 * time.Millisecond
	destroyAbsencePoll = 100 * time.Millisecond
)

type FCConfig struct {
	APIURL          string
	APIKey          string
	VPCID           string
	SecurityGroupID string
	VSwitchIDs      []string
	TimeoutSeconds  int
}

// FCProvider uses the E2B HTTP contract directly. In particular, create is
// deliberately NOT retried. The caller persists the intent before this call.
type FCProvider struct {
	config FCConfig
	client *http.Client
}

func NewFCProvider(config FCConfig) (*FCProvider, error) {
	if config.VPCID == "" || config.SecurityGroupID == "" || len(config.VSwitchIDs) == 0 {
		return nil, errors.New("incomplete DSH FC VPC configuration")
	}
	for _, id := range config.VSwitchIDs {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("empty DSH FC vSwitch")
		}
	}
	return newFCTransport(config)
}

func newFCTransport(config FCConfig) (*FCProvider, error) {
	u, err := url.Parse(config.APIURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid DSH FC API URL")
	}
	loopback := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback != nil && loopback.IsLoopback()) {
		return nil, errors.New("DSH FC API requires HTTPS")
	}
	if config.APIKey == "" || config.TimeoutSeconds < 60 {
		return nil, errors.New("incomplete DSH FC API or lifetime configuration")
	}
	config.APIURL = strings.TrimRight(config.APIURL, "/")
	return &FCProvider{config, &http.Client{
		Timeout:       90 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

type volumeMount struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type sandboxInfo struct {
	ID       string            `json:"sandboxID"`
	Template string            `json:"templateID"`
	State    string            `json:"state"`
	Metadata map[string]string `json:"metadata"`
	Mounts   []volumeMount     `json:"volumeMounts"`
}

func identity(h Host) map[string]string {
	labels := map[string]string{
		"multica.dsh.intent":       h.CreateIntent.String(),
		"multica.dsh.workspace":    h.WorkspaceID.String(),
		"multica.dsh.agent":        h.AgentID.String(),
		"multica.dsh.generation":   strconv.FormatInt(h.Generation, 10),
		"multica.dsh.volume":       h.VolumeName,
		"multica.dsh.access-point": h.AccessPointARN,
		"multica.dsh.file-system":  h.FileSystemID,
		"multica.dsh.space":        h.SpaceID,
		"multica.dsh.template-id":  h.TemplateID,
		"multica.dsh.role-arn":     h.RoleARN,
	}
	if h.ScopeID != uuid.Nil {
		labels["multica.filesystem.scope"] = h.ScopeID.String()
	}
	return labels
}

// FCStatusError is a definite HTTP answer from FC. Only the status is kept.
type FCStatusError struct{ Status int }

func (e *FCStatusError) Error() string { return fmt.Sprintf("DSH FC returned HTTP %d", e.Status) }

// createRejected reports a create request FC answered without creating a
// sandbox. Timeouts and server errors stay ambiguous.
func createRejected(err error) (int, bool) {
	var status *FCStatusError
	if !errors.As(err, &status) || status.Status < 400 || status.Status >= 500 || status.Status == http.StatusRequestTimeout {
		return 0, false
	}
	return status.Status, true
}

func (p *FCProvider) request(ctx context.Context, method, path string, body any) ([]byte, http.Header, int, error) {
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return nil, nil, 0, errors.New("encode DSH FC request failed")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, p.config.APIURL+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, nil, 0, errors.New("create DSH FC request failed")
	}
	req.Header.Set("X-API-KEY", p.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return nil, nil, 0, errors.New("DSH FC transport outcome unconfirmed")
	}
	defer res.Body.Close()
	// Never include the response body in errors; provider responses can echo
	// request credentials or signed connection URLs.
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, res.Header, res.StatusCode, &FCStatusError{Status: res.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, nil, res.StatusCode, errors.New("invalid DSH FC response size")
	}
	return data, res.Header, res.StatusCode, nil
}

func (p *FCProvider) Create(ctx context.Context, h Host) (string, error) {
	if h.State != "creating" {
		return "", errors.New("invalid persisted DSH FC create intent")
	}
	if len(h.ExtraMounts) == 1 && h.AuthRoleARN != "" {
		spec, err := DualCreateSpec(h, h.ExtraMounts[0], h.AuthRoleARN)
		if err != nil {
			return "", err
		}
		return p.CreateSpec(ctx, spec)
	}
	spec, err := employeeCreateSpec(h)
	if err != nil {
		return "", err
	}
	return p.CreateSpec(ctx, spec)
}

func (p *FCProvider) InspectMounts(ctx context.Context, id string) ([]VolumeMountSpec, error) {
	if !sandboxIDPattern.MatchString(id) {
		return nil, errors.New("invalid DSH FC sandbox ID")
	}
	data, _, _, err := p.request(ctx, http.MethodGet, "/sandboxes/"+id, nil)
	if err != nil {
		return nil, err
	}
	var info sandboxInfo
	if err := json.Unmarshal(data, &info); err != nil || !sandboxIDPattern.MatchString(info.ID) {
		return nil, errors.New("invalid DSH FC sandbox inspect response")
	}
	out := make([]VolumeMountSpec, 0, len(info.Mounts))
	for _, m := range info.Mounts {
		out = append(out, VolumeMountSpec{Name: m.Name, Path: m.Path})
	}
	return out, nil
}

// CreateSpec posts a generalized mount list. Employee Create remains the
// single /mnt/multica specialization so existing DSH sandboxes are unchanged.
func (p *FCProvider) CreateSpec(ctx context.Context, spec SandboxCreateSpec) (string, error) {
	if err := spec.valid(); err != nil {
		return "", err
	}
	metadata := make(map[string]string, len(spec.Labels)+2)
	for key, value := range spec.Labels {
		metadata[key] = value
	}
	vpc, err := json.Marshal(map[string]any{"vpcId": p.config.VPCID, "securityGroupId": p.config.SecurityGroupID, "vSwitchIds": p.config.VSwitchIDs})
	if err != nil {
		return "", err
	}
	metadata["fc.sandbox.network.vpc"] = string(vpc)
	metadata["fc.sandbox.auth.role"] = spec.RoleARN
	mounts := make([]volumeMount, len(spec.Mounts))
	for i, m := range spec.Mounts {
		mounts[i] = volumeMount{Name: m.Name, Path: m.Path}
	}
	data, _, _, err := p.request(ctx, http.MethodPost, "/sandboxes", map[string]any{
		"templateID": spec.TemplateID, "timeout": SandboxTaskTimeoutSeconds(p.config.TimeoutSeconds),
		"autoPause": false,
		"metadata":  metadata, "volumeMounts": mounts,
	})
	if err != nil {
		return "", err
	}
	var info sandboxInfo
	if err := json.Unmarshal(data, &info); err != nil || !sandboxIDPattern.MatchString(info.ID) {
		return "", errors.New("DSH FC create returned no valid sandbox ID")
	}
	return info.ID, nil
}

func (p *FCProvider) Healthy(ctx context.Context, id string) error {
	if !sandboxIDPattern.MatchString(id) {
		return errors.New("invalid DSH FC sandbox ID")
	}
	data, _, _, err := p.request(ctx, http.MethodGet, "/sandboxes/"+id, nil)
	if err != nil {
		return err
	}
	var info sandboxInfo
	if err := json.Unmarshal(data, &info); err != nil || info.ID != id || info.State != "running" {
		return errors.New("DSH FC sandbox is not confirmed running")
	}
	return nil
}

func (p *FCProvider) DestroyAndConfirmAbsent(ctx context.Context, id string) error {
	if !sandboxIDPattern.MatchString(id) {
		return errors.New("invalid DSH FC sandbox ID")
	}
	_, _, status, err := p.request(ctx, http.MethodDelete, "/sandboxes/"+id, nil)
	if err != nil && status != http.StatusNotFound {
		return err
	}
	confirmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), destroyAbsenceWait)
	defer cancel()
	for {
		if p.sandboxAbsent(confirmCtx, id) {
			return nil
		}
		select {
		case <-confirmCtx.Done():
			return errors.New("DSH FC old sandbox absence is unconfirmed")
		case <-time.After(destroyAbsencePoll):
		}
	}
}

func (p *FCProvider) sandboxAbsent(ctx context.Context, id string) bool {
	_, _, status, _ := p.request(ctx, http.MethodGet, "/sandboxes/"+id, nil)
	return status == http.StatusNotFound
}

func (p *FCProvider) SandboxAbsent(ctx context.Context, id string) (bool, error) {
	if !sandboxIDPattern.MatchString(id) {
		return false, errors.New("invalid DSH FC sandbox ID")
	}
	return p.sandboxAbsent(ctx, id), nil
}

func matches(info sandboxInfo, h Host) bool {
	spec, err := employeeCreateSpec(h)
	if err != nil {
		return false
	}
	if matchesSpec(info, spec) {
		return true
	}
	if len(info.Mounts) != 2 {
		return false
	}
	var shared VolumeMountSpec
	hasPrivate := false
	for _, m := range info.Mounts {
		if m.Path == MountPath && m.Name == h.VolumeName {
			hasPrivate = true
		} else if m.Path == WorkspaceSharedRoot && m.Name != "" && m.Name != h.VolumeName {
			shared = VolumeMountSpec{Name: m.Name, Path: m.Path}
		}
	}
	if !hasPrivate || shared.Name == "" {
		return false
	}
	dual, err := DualCreateSpec(h, shared, info.Metadata["fc.sandbox.auth.role"])
	return err == nil && matchesSpec(info, dual)
}

func (p *FCProvider) FindCreated(ctx context.Context, h Host) (string, error) {
	if len(h.ExtraMounts) == 1 && h.AuthRoleARN != "" {
		spec, err := DualCreateSpec(h, h.ExtraMounts[0], h.AuthRoleARN)
		if err != nil {
			return "", err
		}
		return p.FindCreatedSpec(ctx, spec)
	}
	return p.findSandbox(ctx, "multica.dsh.intent", h.CreateIntent.String(), func(info sandboxInfo) bool { return matches(info, h) })
}

func (p *FCProvider) FindCreatedSpec(ctx context.Context, spec SandboxCreateSpec) (string, error) {
	if err := spec.valid(); err != nil {
		return "", err
	}
	label, value := "multica.dsh.intent", spec.CreateIntent.String()
	if spec.Scope == "wsfs-read" || spec.Scope == "wsfs-write" {
		label, value = "multica.wsfs.intent", spec.CreateIntent.String()
	}
	return p.findSandbox(ctx, label, value, func(info sandboxInfo) bool { return matchesSpec(info, spec) })
}

func (p *FCProvider) findSandbox(ctx context.Context, label, value string, match func(sandboxInfo) bool) (string, error) {
	query := url.Values{"metadata": {url.Values{label: {value}}.Encode()}, "limit": {"100"}}
	found := ""
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		data, headers, _, err := p.request(ctx, http.MethodGet, "/v2/sandboxes?"+query.Encode(), nil)
		if err != nil {
			return "", err
		}
		var infos []sandboxInfo
		if err := json.Unmarshal(data, &infos); err != nil {
			return "", errors.New("invalid DSH FC sandbox listing")
		}
		for _, info := range infos {
			if info.Metadata[label] != value {
				continue
			}
			if !match(info) || found != "" {
				return "", errors.New("ambiguous DSH FC create intent identity")
			}
			found = info.ID
		}
		next := headers.Get("X-Next-Token")
		if next == "" {
			if found == "" {
				return "", ErrPending
			}
			// Validate the candidate with a direct read, not listing alone.
			data, _, _, err := p.request(ctx, http.MethodGet, "/sandboxes/"+found, nil)
			if err != nil {
				return "", err
			}
			var info sandboxInfo
			if err := json.Unmarshal(data, &info); err != nil || !match(info) {
				return "", ErrPending
			}
			return found, nil
		}
		if seen[next] {
			return "", errors.New("DSH FC pagination did not advance")
		}
		seen[next] = true
		query.Set("nextToken", next)
	}
	return "", errors.New("DSH FC reconciliation exceeded page limit")
}
