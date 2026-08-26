package runtimeconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/multica-ai/multica/server/pkg/modelpricing"
)

const (
	ModelPricingSchemaVersion = 1
	ModelPricingDiamondDataID = "dt-fde-multica-model-pricing.json"
	ModelPricingCurrency      = "USD"
	ModelPricingUnit          = "per_million_tokens"
)

// ModelPricingConfig is the complete deployment-owned price document. Rates
// are USD per million tokens so every existing Multica cost surface can keep
// using its established 1e-10 USD tick contract.
type ModelPricingConfig struct {
	Version  int                  `json:"version"`
	Currency string               `json:"currency"`
	Unit     string               `json:"unit"`
	Models   modelpricing.Catalog `json:"models"`
}

type ModelPricingSnapshot struct {
	Version    int
	Currency   string
	Unit       string
	Models     modelpricing.Catalog
	SQLJSON    json.RawMessage
	Generation uint64
	SHA256     string
}

func ParseModelPricingStrict(data []byte) (ModelPricingConfig, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg ModelPricingConfig
	if err := decoder.Decode(&cfg); err != nil {
		return ModelPricingConfig{}, fmt.Errorf("parse model pricing: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ModelPricingConfig{}, errors.New("model pricing contains multiple JSON values")
		}
		return ModelPricingConfig{}, fmt.Errorf("parse trailing model pricing: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return ModelPricingConfig{}, err
	}
	cfg.Models = cfg.Models.Normalize()
	return cloneModelPricingConfig(cfg), nil
}

func (c ModelPricingConfig) Validate() error {
	if c.Version != ModelPricingSchemaVersion {
		return fmt.Errorf("model pricing version must be %d", ModelPricingSchemaVersion)
	}
	if c.Currency != ModelPricingCurrency {
		return fmt.Errorf("model pricing currency must be %q", ModelPricingCurrency)
	}
	if c.Unit != ModelPricingUnit {
		return fmt.Errorf("model pricing unit must be %q", ModelPricingUnit)
	}
	if len(c.Models) == 0 {
		return errors.New("model pricing must not be empty")
	}
	seen := make(map[string]struct{}, len(c.Models))
	for model, rate := range c.Models {
		normalized := strings.ToLower(strings.TrimSpace(model))
		if model == "" || normalized != model {
			return errors.New("model pricing keys must be lowercase model IDs without surrounding whitespace")
		}
		if _, exists := seen[normalized]; exists {
			return fmt.Errorf("duplicate normalized model pricing key %q", normalized)
		}
		seen[normalized] = struct{}{}
		values := []float64{rate.Input, rate.Output, rate.CacheRead, rate.CacheWrite}
		for _, value := range values {
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return fmt.Errorf("model pricing %q contains an invalid rate", model)
			}
		}
		if rate.Input == 0 && rate.Output == 0 && rate.CacheRead == 0 && rate.CacheWrite == 0 {
			return fmt.Errorf("model pricing %q must contain at least one positive rate", model)
		}
		if rate.BaseTierMaxInputTokens < 0 {
			return fmt.Errorf("model pricing %q base tier limit must not be negative", model)
		}
	}
	return nil
}

// ValidateModels requires every selectable managed model to have a price.
// Price-first updates may safely leave extra catalog entries for future models.
func (c ModelPricingConfig) ValidateModels(models []string) error {
	for _, model := range models {
		model = strings.ToLower(strings.TrimSpace(model))
		if _, ok := c.Models[model]; !ok {
			return fmt.Errorf("managed model %q has no Diamond price", model)
		}
	}
	return nil
}

func (s *Service) ModelPricing() ModelPricingSnapshot {
	if s == nil {
		return ModelPricingSnapshot{}
	}
	current := s.modelPricing.Load()
	if current == nil {
		return ModelPricingSnapshot{}
	}
	return cloneModelPricingSnapshot(*current)
}

func (s *Service) ApplyModelPricingJSON(data []byte) (ModelPricingSnapshot, error) {
	if s == nil {
		return ModelPricingSnapshot{}, errors.New("runtime config service is nil")
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	cfg, err := ParseModelPricingStrict(data)
	if err != nil {
		return s.ModelPricing(), err
	}
	if current := s.Current(); current.Generation > 0 {
		if err := cfg.ValidateModels(current.Config.Runtime.LLM.Models); err != nil {
			return s.ModelPricing(), err
		}
	}
	sqlJSON, err := cfg.Models.SQLJSON()
	if err != nil {
		return s.ModelPricing(), fmt.Errorf("encode model pricing for SQL: %w", err)
	}
	previous := s.modelPricing.Load()
	generation := uint64(1)
	if previous != nil {
		generation = previous.Generation + 1
	}
	sum := sha256.Sum256(data)
	next := &ModelPricingSnapshot{
		Version:    cfg.Version,
		Currency:   cfg.Currency,
		Unit:       cfg.Unit,
		Models:     cfg.Models.Clone(),
		SQLJSON:    append(json.RawMessage(nil), sqlJSON...),
		Generation: generation,
		SHA256:     hex.EncodeToString(sum[:]),
	}
	s.modelPricing.Store(next)
	return cloneModelPricingSnapshot(*next), nil
}

func cloneModelPricingConfig(in ModelPricingConfig) ModelPricingConfig {
	out := in
	out.Models = in.Models.Clone()
	return out
}

func cloneModelPricingSnapshot(in ModelPricingSnapshot) ModelPricingSnapshot {
	out := in
	out.Models = in.Models.Clone()
	out.SQLJSON = append(json.RawMessage(nil), in.SQLJSON...)
	return out
}
