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

const MountPath = "/mnt/multica-dsh"

var sandboxIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

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
	u, err := url.Parse(config.APIURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid DSH FC API URL")
	}
	loopback := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback != nil && loopback.IsLoopback()) {
		return nil, errors.New("DSH FC API requires HTTPS")
	}
	if config.APIKey == "" || config.VPCID == "" || config.SecurityGroupID == "" || len(config.VSwitchIDs) == 0 || config.TimeoutSeconds < 60 {
		return nil, errors.New("incomplete DSH FC API, VPC or lifetime configuration")
	}
	for _, id := range config.VSwitchIDs {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("empty DSH FC vSwitch")
		}
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
	return map[string]string{
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
		return nil, res.Header, res.StatusCode, fmt.Errorf("DSH FC returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, nil, res.StatusCode, errors.New("invalid DSH FC response size")
	}
	return data, res.Header, res.StatusCode, nil
}

func (p *FCProvider) Create(ctx context.Context, h Host) (string, error) {
	if h.State != "creating" || h.CreateIntent == uuid.Nil || h.WorkspaceID == uuid.Nil || h.AgentID == uuid.Nil || h.Generation < 1 ||
		h.VolumeName == "" || h.AccessPointARN == "" || h.RoleARN == "" || !sandboxIDPattern.MatchString(h.TemplateID) {
		return "", errors.New("invalid persisted DSH FC create intent")
	}
	metadata := identity(h)
	vpc, err := json.Marshal(map[string]any{"vpcId": p.config.VPCID, "securityGroupId": p.config.SecurityGroupID, "vSwitchIds": p.config.VSwitchIDs})
	if err != nil {
		return "", err
	}
	metadata["fc.sandbox.network.vpc"] = string(vpc)
	metadata["fc.sandbox.auth.role"] = h.RoleARN
	data, _, _, err := p.request(ctx, http.MethodPost, "/sandboxes", map[string]any{
		"templateID": h.TemplateID, "timeout": p.config.TimeoutSeconds,
		"autoPause": false,
		"metadata":  metadata, "volumeMounts": []volumeMount{{h.VolumeName, MountPath}},
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
	// A success/accepted response alone does not fence a mounted writer.
	// Require a subsequent authoritative lookup of this exact sandbox ID.
	_, _, status, _ = p.request(ctx, http.MethodGet, "/sandboxes/"+id, nil)
	if status != http.StatusNotFound {
		return errors.New("DSH FC old sandbox absence is unconfirmed")
	}
	return nil
}

func matches(info sandboxInfo, h Host) bool {
	// FC reports a display alias in templateID and consumes fc.sandbox.*
	// metadata during creation. Match the immutable requested template and
	// role via our own durable labels, plus the actual returned mount.
	if !sandboxIDPattern.MatchString(info.ID) || info.Template == "" {
		return false
	}
	for key, value := range identity(h) {
		if info.Metadata[key] != value {
			return false
		}
	}
	return len(info.Mounts) == 1 && info.Mounts[0].Name == h.VolumeName && info.Mounts[0].Path == MountPath
}

func (p *FCProvider) FindCreated(ctx context.Context, h Host) (string, error) {
	query := url.Values{"metadata": {url.Values{"multica.dsh.intent": {h.CreateIntent.String()}}.Encode()}, "limit": {"100"}}
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
			if info.Metadata["multica.dsh.intent"] != h.CreateIntent.String() {
				continue
			}
			if !matches(info, h) || found != "" {
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
			if err := json.Unmarshal(data, &info); err != nil || !matches(info, h) {
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
