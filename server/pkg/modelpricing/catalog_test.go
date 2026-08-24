package modelpricing

import (
	"encoding/json"
	"testing"
)

func testCatalog() Catalog {
	return Catalog{
		"qwen3.8-max":  {Input: 1.768842, Output: 5.306526, CacheRead: 0.221105, CacheWrite: 2.211052},
		"qwen3.7-plus": {Input: 0.294807, Output: 1.179228, CacheRead: 0.058961, CacheWrite: 0.368509},
	}
}

func TestCatalogResolveExactTransportAliasesOnly(t *testing.T) {
	catalog := testCatalog()
	for _, model := range []string{"qwen3.8-max", "custom:qwen3.8-max", "provider/qwen3.8-max", "PROVIDER/CUSTOM:QWEN3.8-MAX"} {
		if _, ok := catalog.Resolve(model); !ok {
			t.Fatalf("Resolve(%q) did not find managed model", model)
		}
	}
	for _, model := range []string{"qwen3.7-plus", "custom:qwen3.7-plus", "qwen/custom:qwen3.7-plus"} {
		if _, ok := catalog.Resolve(model); !ok {
			t.Fatalf("Resolve(%q) did not find qwen3.7-plus", model)
		}
	}
	for _, model := range []string{"qwen3.8-max-preview", "qwen3.8", "qwen3.8-max-20260824"} {
		if _, ok := catalog.Resolve(model); ok {
			t.Fatalf("Resolve(%q) unexpectedly used fuzzy pricing", model)
		}
	}
}

func TestCatalogApplyKeepsProviderCostWhenNoTokensRemainUncosted(t *testing.T) {
	cost, input, output, cacheRead, cacheWrite, priced := testCatalog().Apply(
		"qwen3.8-max", 987654321, 0, 0, 0, 0,
	)
	if priced || cost != 987654321 || input != 0 || output != 0 || cacheRead != 0 || cacheWrite != 0 {
		t.Fatalf("provider-priced result = %d %d/%d/%d/%d priced=%v", cost, input, output, cacheRead, cacheWrite, priced)
	}
}

func TestCatalogApplyAddsEstimateToProviderCost(t *testing.T) {
	cost, input, output, cacheRead, cacheWrite, priced := testCatalog().Apply(
		"custom:qwen3.8-max",
		1_000,
		1_000_000,
		1_000_000,
		1_000_000,
		1_000_000,
	)
	if !priced {
		t.Fatal("managed model was not priced")
	}
	want := int64((1.768842 + 5.306526 + 0.221105 + 2.211052) * CostUSDTicksPerUSD)
	if cost != 1_000+want {
		t.Fatalf("cost = %d, want %d", cost, 1_000+want)
	}
	if input != 0 || output != 0 || cacheRead != 0 || cacheWrite != 0 {
		t.Fatalf("priced tokens remain uncosted: %d/%d/%d/%d", input, output, cacheRead, cacheWrite)
	}
}

func TestCatalogApplyRetainsUnknownTokens(t *testing.T) {
	cost, input, output, cacheRead, cacheWrite, priced := testCatalog().Apply("unknown", 9, 1, 2, 3, 4)
	if priced || cost != 9 || input != 1 || output != 2 || cacheRead != 3 || cacheWrite != 4 {
		t.Fatalf("unknown result = %d %d/%d/%d/%d priced=%v", cost, input, output, cacheRead, cacheWrite, priced)
	}
}

func TestCatalogSQLJSONContainsHermesAlias(t *testing.T) {
	raw, err := testCatalog().SQLJSON()
	if err != nil {
		t.Fatalf("SQLJSON: %v", err)
	}
	var aliases Catalog
	if err := json.Unmarshal(raw, &aliases); err != nil {
		t.Fatalf("decode aliases: %v", err)
	}
	if len(aliases) != 4 || aliases["custom:qwen3.8-max"].Output != 5.306526 ||
		aliases["custom:qwen3.7-plus"].Output != 1.179228 {
		t.Fatalf("aliases = %#v", aliases)
	}
}
