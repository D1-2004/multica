package runtimeconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	EnvSource     = "MULTICA_RUNTIME_CONFIG_SOURCE"
	SourceDiamond = "diamond"
	DiamondDataID = "dt-fde-multica-runtime.json"
	DiamondGroup  = "DEFAULT_GROUP"
)

type Snapshot struct {
	Config     Config
	Generation uint64
	SHA256     string
}

type Service struct {
	snapshot         atomic.Pointer[Snapshot]
	runtimeProviders atomic.Pointer[RuntimeProvidersSnapshot]
	modelPricing     atomic.Pointer[ModelPricingSnapshot]
	logger           *slog.Logger
	prod             bool

	// applyMu serializes runtime-model and pricing-catalog updates. Each
	// document validates against the other's current snapshot, so separate
	// locks could admit two individually valid updates as one invalid pair.
	applyMu                 sync.Mutex
	runtimeProvidersApplyMu sync.Mutex
	mu                      sync.Mutex
	closeOnce               sync.Once
	closeFunc               func() error
	closeErr                error
	validator               func(Config) error
	subscribers             []func(Snapshot)
}

func NewStatic(cfg Config) (*Service, error) {
	if err := cfg.Validate(false); err != nil {
		return nil, err
	}
	service := &Service{}
	encoded, err := jsonForDigest(cfg.normalized())
	if err != nil {
		return nil, err
	}
	service.store(cfg.normalized(), encoded)
	return service, nil
}

// NewFromEnv activates the required Diamond source only when explicitly set.
// An empty source means the caller should retain the existing environment
// configuration path; it is a deployment mode, not a fail-open data source.
func NewFromEnv(logger *slog.Logger) (*Service, error) {
	source := strings.ToLower(strings.TrimSpace(os.Getenv(EnvSource)))
	if source == "" {
		return nil, nil
	}
	if source != SourceDiamond {
		return nil, fmt.Errorf("%s must be empty or %q", EnvSource, SourceDiamond)
	}
	production := productionEnvironmentFromEnv()
	return newDiamondService(logger, production, newNacosDiamondClient)
}

func productionEnvironmentFromEnv() bool {
	for _, name := range []string{"AONE_ENV_TYPE", "ENV_TYPE", "GO_ENV", "APP_ENV"} {
		raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
		if raw == "" {
			continue
		}
		switch raw {
		case "prod", "production", "online":
			return true
		default:
			return false
		}
	}
	return false
}

func (s *Service) Current() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	current := s.snapshot.Load()
	if current == nil {
		return Snapshot{}
	}
	return cloneSnapshot(*current)
}

func (s *Service) ApplyJSON(data []byte) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, errors.New("runtime config service is nil")
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	cfg, err := ParseStrict(data, s.prod)
	if err != nil {
		return s.Current(), err
	}
	if pricing := s.ModelPricing(); pricing.Generation > 0 {
		pricingConfig := ModelPricingConfig{
			Version: pricing.Version, Currency: pricing.Currency, Unit: pricing.Unit, Models: pricing.Models,
		}
		if err := pricingConfig.ValidateModels(cfg.Runtime.LLM.Models); err != nil {
			return s.Current(), err
		}
	}
	s.mu.Lock()
	validator := s.validator
	s.mu.Unlock()
	if validator != nil {
		if err := validator(cfg); err != nil {
			return s.Current(), fmt.Errorf("validate runtime config dependencies: %w", err)
		}
	}
	return s.store(cfg, data), nil
}

// SetValidator installs deployment-specific validation such as required
// secret presence. It validates the current snapshot before becoming active,
// so every later snapshot has the same acceptance contract.
func (s *Service) SetValidator(validator func(Config) error) error {
	if s == nil || validator == nil {
		return nil
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	current := s.Current()
	if current.Generation == 0 {
		return errors.New("runtime config has no current snapshot")
	}
	if err := validator(current.Config); err != nil {
		return err
	}
	s.mu.Lock()
	s.validator = validator
	s.mu.Unlock()
	return nil
}

func (s *Service) store(cfg Config, encoded []byte) Snapshot {
	previous := s.snapshot.Load()
	generation := uint64(1)
	if previous != nil {
		generation = previous.Generation + 1
	}
	sum := sha256.Sum256(encoded)
	next := &Snapshot{
		Config:     cfg.normalized(),
		Generation: generation,
		SHA256:     hex.EncodeToString(sum[:]),
	}
	s.snapshot.Store(next)
	result := cloneSnapshot(*next)
	s.mu.Lock()
	subscribers := append([]func(Snapshot){}, s.subscribers...)
	s.mu.Unlock()
	for _, subscriber := range subscribers {
		subscriber(cloneSnapshot(result))
	}
	return result
}

// Subscribe registers a lightweight post-commit observer and immediately
// supplies the current snapshot. Observers must not block or call ApplyJSON.
func (s *Service) Subscribe(subscriber func(Snapshot)) {
	if s == nil || subscriber == nil {
		return
	}
	s.mu.Lock()
	s.subscribers = append(s.subscribers, subscriber)
	s.mu.Unlock()
	if current := s.Current(); current.Generation > 0 {
		subscriber(current)
	}
}

func (s *Service) setCloseFunc(fn func() error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closeFunc = fn
	s.mu.Unlock()
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		fn := s.closeFunc
		s.mu.Unlock()
		if fn != nil {
			s.closeErr = fn()
		}
	})
	return s.closeErr
}

func cloneSnapshot(in Snapshot) Snapshot {
	out := in
	if in.Config.Features.InternalMCPConnectors != nil {
		enabled := *in.Config.Features.InternalMCPConnectors
		out.Config.Features.InternalMCPConnectors = &enabled
	}
	if in.Config.Features.SemanticaMCPRelay != nil {
		enabled := *in.Config.Features.SemanticaMCPRelay
		out.Config.Features.SemanticaMCPRelay = &enabled
	}
	if in.Config.Runtime.AgenticFS.Placement != nil {
		placement := *in.Config.Runtime.AgenticFS.Placement
		placement.VSwitchIDs = append([]string(nil), placement.VSwitchIDs...)
		out.Config.Runtime.AgenticFS.Placement = &placement
	}
	out.Config.Web.SiteConnectSrc = append([]string(nil), in.Config.Web.SiteConnectSrc...)
	out.Config.Web.CORSAllowedOrigins = append([]string(nil), in.Config.Web.CORSAllowedOrigins...)
	out.Config.Web.LoginProviders = append([]string(nil), in.Config.Web.LoginProviders...)
	out.Config.Runtime.LLM.Models = append([]string(nil), in.Config.Runtime.LLM.Models...)
	if in.Config.Runtime.PerformanceOptimization != nil {
		rollout := *in.Config.Runtime.PerformanceOptimization
		rollout.AgentIDs = append([]string(nil), rollout.AgentIDs...)
		if rollout.FinishSchemaExperiment != nil {
			experiment := *rollout.FinishSchemaExperiment
			rollout.FinishSchemaExperiment = &experiment
		}
		out.Config.Runtime.PerformanceOptimization = &rollout
	}
	out.Config.Runtime.FCE2B.StablePublisherUserIDs = append([]string(nil), in.Config.Runtime.FCE2B.StablePublisherUserIDs...)
	out.Config.AgentIdentity.DebugContextTokenAgents = append([]string(nil), in.Config.AgentIdentity.DebugContextTokenAgents...)
	out.Config.EnterpriseIdentity.BUCAuthorizeApps = append([]string(nil), in.Config.EnterpriseIdentity.BUCAuthorizeApps...)
	out.Config.Runtime.FCE2BSDKRollout = in.Config.Runtime.FCE2BSDKRollout.clone()
	return out
}

func jsonForDigest(cfg Config) ([]byte, error) {
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal runtime config digest: %w", err)
	}
	return encoded, nil
}
