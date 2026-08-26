package handler

import "github.com/multica-ai/multica/server/pkg/modelpricing"

type usageCostSplit struct {
	CostUSDTicks             int64
	UncostedInputTokens      int64
	UncostedOutputTokens     int64
	UncostedCacheReadTokens  int64
	UncostedCacheWriteTokens int64
}

func applyModelPricing(catalog modelpricing.Catalog, model string, split usageCostSplit) usageCostSplit {
	if len(catalog) == 0 {
		return split
	}
	cost, input, output, cacheRead, cacheWrite, _ := catalog.Apply(
		model,
		split.CostUSDTicks,
		split.UncostedInputTokens,
		split.UncostedOutputTokens,
		split.UncostedCacheReadTokens,
		split.UncostedCacheWriteTokens,
	)
	return usageCostSplit{
		CostUSDTicks:             cost,
		UncostedInputTokens:      input,
		UncostedOutputTokens:     output,
		UncostedCacheReadTokens:  cacheRead,
		UncostedCacheWriteTokens: cacheWrite,
	}
}
