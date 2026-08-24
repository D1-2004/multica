package modelpricing

import (
	"encoding/json"
	"math"
	"strings"
)

const (
	CostUSDTicksPerUSD = 10_000_000_000
	tokensPerMillion   = 1_000_000
)

// Rate is one model's USD price per million tokens. BaseTierMaxInputTokens is
// non-zero when the source provider publishes request-size tiers and this row
// represents the first tier used for aggregate cost estimation.
type Rate struct {
	Input                  float64 `json:"input"`
	Output                 float64 `json:"output"`
	CacheRead              float64 `json:"cache_read"`
	CacheWrite             float64 `json:"cache_write"`
	BaseTierMaxInputTokens int64   `json:"base_tier_max_input_tokens,omitempty"`
}

// Catalog is keyed by the exact deployment-owned model ID.
type Catalog map[string]Rate

func (c Catalog) Clone() Catalog {
	if c == nil {
		return nil
	}
	out := make(Catalog, len(c))
	for model, rate := range c {
		out[model] = rate
	}
	return out
}

// Normalize returns a lowercase, whitespace-trimmed copy. Managed model IDs
// are case-insensitive on the usage boundary and Diamond rejects collisions
// before a normalized catalog is installed.
func (c Catalog) Normalize() Catalog {
	out := make(Catalog, len(c))
	for model, rate := range c {
		out[strings.ToLower(strings.TrimSpace(model))] = rate
	}
	return out
}

// Resolve tolerates transport-only prefixes without making fuzzy model-family
// guesses. Hermes reports managed models as custom:<id>; some CLIs report a
// provider/<id> path. Both still refer to the exact Diamond model ID.
func (c Catalog) Resolve(model string) (Rate, bool) {
	_, rate, ok := c.ResolveModel(model)
	return rate, ok
}

// ResolveModel also returns the canonical Diamond model ID for metrics and
// user-visible catalogs.
func (c Catalog) ResolveModel(model string) (string, Rate, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return "", Rate{}, false
	}
	candidates := []string{model}
	if stripped := strings.TrimPrefix(model, "custom:"); stripped != model {
		candidates = append(candidates, stripped)
	}
	if slash := strings.IndexByte(model, '/'); slash > 0 && slash+1 < len(model) {
		stripped := model[slash+1:]
		candidates = append(candidates, stripped, strings.TrimPrefix(stripped, "custom:"))
	}
	for _, candidate := range candidates {
		if rate, ok := c[candidate]; ok {
			return candidate, rate, true
		}
	}
	return "", Rate{}, false
}

// Apply prices only the token portion that did not carry a provider-reported
// cost. Provider cost remains authoritative and is added unchanged.
func (c Catalog) Apply(
	model string,
	costUSDTicks int64,
	uncostedInputTokens int64,
	uncostedOutputTokens int64,
	uncostedCacheReadTokens int64,
	uncostedCacheWriteTokens int64,
) (int64, int64, int64, int64, int64, bool) {
	if uncostedInputTokens == 0 && uncostedOutputTokens == 0 &&
		uncostedCacheReadTokens == 0 && uncostedCacheWriteTokens == 0 {
		return costUSDTicks, 0, 0, 0, 0, false
	}
	rate, ok := c.Resolve(model)
	if !ok {
		return costUSDTicks, uncostedInputTokens, uncostedOutputTokens,
			uncostedCacheReadTokens, uncostedCacheWriteTokens, false
	}
	estimate := (float64(uncostedInputTokens)*rate.Input +
		float64(uncostedOutputTokens)*rate.Output +
		float64(uncostedCacheReadTokens)*rate.CacheRead +
		float64(uncostedCacheWriteTokens)*rate.CacheWrite) /
		tokensPerMillion * CostUSDTicksPerUSD
	if math.IsNaN(estimate) || math.IsInf(estimate, 0) || estimate < 0 ||
		estimate > float64(math.MaxInt64-costUSDTicks) {
		return costUSDTicks, uncostedInputTokens, uncostedOutputTokens,
			uncostedCacheReadTokens, uncostedCacheWriteTokens, false
	}
	return costUSDTicks + int64(math.Round(estimate)), 0, 0, 0, 0, true
}

// SQLJSON returns the exact lookup map consumed by label-usage SQL. Alias rows
// are materialized here so SQL can remain an exact key lookup.
func (c Catalog) SQLJSON() ([]byte, error) {
	aliases := make(Catalog, len(c)*2)
	for model, rate := range c.Normalize() {
		aliases[model] = rate
		aliases["custom:"+model] = rate
	}
	return json.Marshal(aliases)
}
