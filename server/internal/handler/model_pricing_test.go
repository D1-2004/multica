package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/pkg/modelpricing"
)

func TestApplyModelPricingCompletesOnlyUncostedTokens(t *testing.T) {
	catalog := modelpricing.Catalog{
		"qwen3.8-max": {Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 1.25},
	}
	got := applyModelPricing(catalog, "custom:qwen3.8-max", usageCostSplit{
		CostUSDTicks:             100,
		UncostedInputTokens:      1_000_000,
		UncostedOutputTokens:     1_000_000,
		UncostedCacheReadTokens:  1_000_000,
		UncostedCacheWriteTokens: 1_000_000,
	})
	wantCost := int64(4.35 * modelpricing.CostUSDTicksPerUSD)
	if got.CostUSDTicks != 100+wantCost {
		t.Fatalf("cost = %d, want %d", got.CostUSDTicks, 100+wantCost)
	}
	if got.UncostedInputTokens != 0 || got.UncostedOutputTokens != 0 ||
		got.UncostedCacheReadTokens != 0 || got.UncostedCacheWriteTokens != 0 {
		t.Fatalf("uncosted split = %#v", got)
	}
}
