// Package modelregistry owns environment-wide provider configuration and routing.
package modelregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	"github.com/multica-ai/multica/server/pkg/llm"
)

type Ref struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (r Ref) String() string { return r.Provider + "/" + r.Model }

type Provider struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	BaseURL string   `json:"base_url"`
	Models  []string `json:"models"`
	Enabled bool     `json:"enabled"`
	Builtin bool     `json:"builtin"`
	HasKey  bool     `json:"has_key"`
	APIKey  string   `json:"api_key,omitempty"`
}
type Config struct {
	Revision        int64      `json:"revision"`
	Providers       []Provider `json:"providers"`
	AgentModels     []Ref      `json:"agent_models"`
	DefaultModel    Ref        `json:"default_model"`
	Coordinator     []Ref      `json:"coordinator"`
	DiamondFallback bool       `json:"diamond_fallback"`
}
type stored struct {
	Config  Config            `json:"config"`
	Secrets map[string][]byte `json:"secrets"`
}
type Snapshot struct {
	Config Config
	Keys   map[string]string
}
type Registry struct {
	// Package tests may replace only the transport after URL validation.
	// Production keeps HTTPClient DNS pinning and redirect protection.
	coordinatorTransport http.RoundTripper
	Pool                 *pgxpool.Pool
	Box                  *secretbox.Box
	Defaults             func() Snapshot
}

var ErrConflict = errors.New("model configuration changed; reload before saving")
var slug = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

func (r *Registry) Load(ctx context.Context) (Snapshot, error) {
	d := r.Defaults()
	var raw []byte
	err := r.Pool.QueryRow(ctx, `SELECT document FROM global_model_configuration ORDER BY revision DESC LIMIT 1`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	var s stored
	if err = json.Unmarshal(raw, &s); err != nil {
		return Snapshot{}, err
	}
	out := Snapshot{Config: s.Config, Keys: map[string]string{}}
	out.Config.Providers = append([]Provider{d.Config.Providers[0]}, s.Config.Providers...)
	out.Keys[d.Config.Providers[0].ID] = d.Keys[d.Config.Providers[0].ID]
	for id, v := range s.Secrets {
		if r.Box == nil {
			return Snapshot{}, errors.New("provider encryption unavailable")
		}
		p, e := r.Box.Open(v)
		if e != nil {
			return Snapshot{}, e
		}
		out.Keys[id] = string(p)
	}
	for i := range out.Config.Providers {
		out.Config.Providers[i].APIKey = ""
		out.Config.Providers[i].HasKey = out.Keys[out.Config.Providers[i].ID] != ""
	}
	if out.Config.DiamondFallback {
		for _, ref := range d.Config.Coordinator {
			if !containsRef(out.Config.Coordinator, ref) {
				out.Config.Coordinator = append(out.Config.Coordinator, ref)
			}
		}
	}
	return out, nil
}
func containsRef(refs []Ref, ref Ref) bool {
	for _, v := range refs {
		if v == ref {
			return true
		}
	}
	return false
}
func (s Snapshot) Resolve(ref Ref) (Provider, string, error) {
	for _, p := range s.Config.Providers {
		if p.ID == ref.Provider && p.Enabled {
			for _, m := range p.Models {
				if m == ref.Model {
					return p, s.Keys[p.ID], nil
				}
			}
		}
	}
	return Provider{}, "", fmt.Errorf("model %s is unavailable", ref.String())
}
func (s Snapshot) AgentRef(model string) (Ref, error) {
	if model == "" {
		return s.Config.DefaultModel, nil
	}
	for _, ref := range s.Config.AgentModels {
		if ref.String() == model || (ref.Provider == "mass" && ref.Model == model) {
			return ref, nil
		}
	}
	return Ref{}, errors.New("model is not in the agent catalog")
}
func (s Snapshot) Client(ref Ref) (*llm.Client, error) {
	p, k, e := s.Resolve(ref)
	if e != nil {
		return nil, e
	}
	return llm.New(llm.Config{BaseURL: p.BaseURL, APIKey: k, DefaultModel: ref.Model, MaxRetries: -1, HTTPClient: HTTPClient()}), nil
}

// Reject local/metadata endpoints, pin DNS resolution per connection, and never forward keys on redirects.
func ValidateURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("base URL must be an HTTPS API root without credentials, query or fragment")
	}
	if badHost(u.Hostname()) {
		return errors.New("local and metadata endpoints are not allowed")
	}
	return nil
}
func badHost(h string) bool {
	ip := net.ParseIP(h)
	return h == "100.100.100.200" || h == "168.63.129.16" || strings.EqualFold(h, "localhost") || strings.HasSuffix(strings.ToLower(h), ".localhost") || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()))
}
func HTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		h, p, e := net.SplitHostPort(addr)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, h)
		if e != nil {
			return nil, e
		}
		for _, ip := range ips {
			if badHost(ip.IP.String()) || (ip.IP.IsPrivate() && !strings.HasSuffix(h, ".dingtalk.com") && !strings.HasSuffix(h, ".alibaba-inc.com")) {
				return nil, errors.New("unsafe provider address")
			}
		}
		var last error
		for _, ip := range ips {
			c, e := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), p))
			if e == nil {
				return c, nil
			}
			last = e
		}
		return nil, last
	}
	return &http.Client{Transport: t, Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (r *Registry) Save(ctx context.Context, c Config, actor string) (Snapshot, error) {
	if r.Box == nil {
		return Snapshot{}, errors.New("provider encryption unavailable")
	}
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return Snapshot{}, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(882929900)`); e != nil {
		return Snapshot{}, e
	}
	old, e := r.Load(ctx)
	if e != nil {
		return Snapshot{}, e
	}
	if c.Revision != old.Config.Revision {
		return Snapshot{}, ErrConflict
	}
	if len(c.Providers) > 30 || len(c.AgentModels) > 300 || len(c.Coordinator) > 5 {
		return Snapshot{}, errors.New("configuration exceeds limits")
	}
	keys := map[string]string{}
	secrets := map[string][]byte{}
	providers := []Provider{}
	seen := map[string]bool{}
	for _, p := range c.Providers {
		if p.ID == "mass" {
			continue
		}
		if !slug.MatchString(p.ID) || seen[p.ID] {
			return Snapshot{}, errors.New("provider identifiers must be unique lowercase slugs")
		}
		seen[p.ID] = true
		if e = ValidateURL(p.BaseURL); e != nil {
			return Snapshot{}, e
		}
		p.BaseURL = strings.TrimRight(p.BaseURL, "/")
		p.Builtin = false
		if len(p.Models) > 500 {
			return Snapshot{}, errors.New("too many models")
		}
		ms := map[string]bool{}
		for _, m := range p.Models {
			if m == "" || strings.TrimSpace(m) != m || len(m) > 200 || ms[m] {
				return Snapshot{}, errors.New("model IDs must be nonempty and unique")
			}
			ms[m] = true
		}
		key := p.APIKey
		if key == "" {
			for _, previous := range old.Config.Providers {
				if previous.ID == p.ID && strings.TrimRight(previous.BaseURL, "/") != p.BaseURL {
					return Snapshot{}, errors.New("changing a provider URL requires a replacement API key")
				}
			}
			key = old.Keys[p.ID]
		}
		if key == "" {
			return Snapshot{}, errors.New("API key is required")
		}
		keys[p.ID] = key
		p.APIKey = ""
		p.HasKey = true
		sealed, e := r.Box.Seal([]byte(key))
		if e != nil {
			return Snapshot{}, e
		}
		secrets[p.ID] = sealed
		providers = append(providers, p)
	}
	c.Providers = providers
	c.Revision++
	effective := Snapshot{Config: c, Keys: keys}
	defaults := r.Defaults()
	effective.Config.Providers = append([]Provider{defaults.Config.Providers[0]}, providers...)
	if c.DiamondFallback {
		for _, v := range defaults.Config.Coordinator {
			if !containsRef(effective.Config.Coordinator, v) {
				effective.Config.Coordinator = append(effective.Config.Coordinator, v)
			}
		}
	}
	seenChain := map[string]bool{}
	for _, ref := range c.Coordinator {
		if seenChain[ref.String()] {
			return Snapshot{}, errors.New("duplicate coordinator model")
		}
		seenChain[ref.String()] = true
	}
	if len(c.AgentModels) == 0 || len(effective.Config.Coordinator) == 0 || !containsRef(c.AgentModels, c.DefaultModel) {
		return Snapshot{}, errors.New("select agent models, a default, and a coordinator model")
	}
	seenRefs := map[string]bool{}
	for _, ref := range c.AgentModels {
		if seenRefs[ref.String()] {
			return Snapshot{}, errors.New("duplicate agent model")
		}
		seenRefs[ref.String()] = true
	}
	for _, ref := range append(append([]Ref{}, c.AgentModels...), effective.Config.Coordinator...) {
		if _, _, e = effective.Resolve(ref); e != nil {
			return Snapshot{}, e
		}
	}
	// Removing a referenced model is rejected; users must migrate agents first.
	for _, ref := range old.Config.AgentModels {
		if containsRef(c.AgentModels, ref) {
			continue
		}
		var used bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent WHERE model=$1 OR ($2='mass' AND model=$3))`, ref.String(), ref.Provider, ref.Model).Scan(&used)
		if e != nil {
			return Snapshot{}, e
		}
		if used {
			return Snapshot{}, fmt.Errorf("model %s is still used by agents", ref.String())
		}
	}
	raw, e := json.Marshal(stored{Config: c, Secrets: secrets})
	if e != nil {
		return Snapshot{}, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO global_model_configuration(revision,document,updated_by) VALUES($1,$2,$3)`, c.Revision, raw, actor)
	if e != nil {
		return Snapshot{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Snapshot{}, e
	}
	return r.Load(ctx)
}

func (r *Registry) ModelForAgent(model string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, e := r.Load(ctx)
	if e != nil {
		return "", e
	}
	ref, e := s.AgentRef(model)
	if e != nil {
		return "", e
	}
	if _, _, e = s.Resolve(ref); e != nil {
		return "", e
	}
	return ref.String(), nil
}

// Restore creates a new audited revision rather than moving the revision backwards.
func (r *Registry) Restore(ctx context.Context, expected int64, actor string) (Snapshot, error) {
	var raw []byte
	e := r.Pool.QueryRow(ctx, `SELECT document FROM global_model_configuration WHERE revision<$1 ORDER BY revision DESC LIMIT 1`, expected).Scan(&raw)
	if e != nil {
		return Snapshot{}, errors.New("no previous configuration revision")
	}
	var saved stored
	if e = json.Unmarshal(raw, &saved); e != nil {
		return Snapshot{}, e
	}
	if r.Box == nil {
		return Snapshot{}, errors.New("provider encryption unavailable")
	}
	for i := range saved.Config.Providers {
		p := &saved.Config.Providers[i]
		key, e := r.Box.Open(saved.Secrets[p.ID])
		if e != nil {
			return Snapshot{}, e
		}
		p.APIKey = string(key)
	}
	saved.Config.Revision = expected
	return r.Save(ctx, saved.Config, actor)
}
