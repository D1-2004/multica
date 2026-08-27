package runtimeconfig

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/modelpricing"
)

func validModelPricingJSON() string {
	return `{
  "version": 1,
  "currency": "USD",
  "unit": "per_million_tokens",
  "models": {
    "qwen3.8-max": {
      "input": 1.768842,
      "output": 5.306526,
      "cache_read": 0.221105,
      "cache_write": 2.211052
    },
    "qwen3-max": {
      "input": 0.368509,
      "output": 1.474035,
      "cache_read": 0.073702,
      "cache_write": 0.460636,
      "base_tier_max_input_tokens": 32000
    }
  }
}`
}

func TestDocumentedModelPricingExampleMatchesSchema(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/runtime-model-pricing.example.json")
	if err != nil {
		t.Fatalf("read documented model pricing: %v", err)
	}
	if _, err := ParseModelPricingStrict(raw); err != nil {
		t.Fatalf("documented model pricing: %v", err)
	}
}

func TestParseModelPricingStrictAcceptsExactContract(t *testing.T) {
	cfg, err := ParseModelPricingStrict([]byte(validModelPricingJSON()))
	if err != nil {
		t.Fatalf("ParseModelPricingStrict: %v", err)
	}
	if cfg.Models["qwen3-max"].BaseTierMaxInputTokens != 32000 {
		t.Fatalf("pricing = %#v", cfg.Models["qwen3-max"])
	}
	if err := cfg.ValidateModels([]string{"qwen3.8-max", "qwen3-max"}); err != nil {
		t.Fatalf("ValidateModels: %v", err)
	}
}

func TestParseModelPricingStrictRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "unknown field", raw: strings.Replace(validModelPricingJSON(), `"version": 1`, `"version": 1, "versoin": 1`, 1)},
		{name: "wrong version", raw: strings.Replace(validModelPricingJSON(), `"version": 1`, `"version": 2`, 1)},
		{name: "wrong currency", raw: strings.Replace(validModelPricingJSON(), `"currency": "USD"`, `"currency": "CNY"`, 1)},
		{name: "wrong unit", raw: strings.Replace(validModelPricingJSON(), `"unit": "per_million_tokens"`, `"unit": "per_token"`, 1)},
		{name: "empty catalog", raw: `{"version":1,"currency":"USD","unit":"per_million_tokens","models":{}}`},
		{name: "uppercase model", raw: strings.Replace(validModelPricingJSON(), `"qwen3.8-max"`, `"Qwen3.8-Max"`, 1)},
		{name: "negative rate", raw: strings.Replace(validModelPricingJSON(), `"input": 1.768842`, `"input": -1`, 1)},
		{name: "all zero", raw: `{"version":1,"currency":"USD","unit":"per_million_tokens","models":{"free":{"input":0,"output":0,"cache_read":0,"cache_write":0}}}`},
		{name: "negative tier", raw: strings.Replace(validModelPricingJSON(), `"base_tier_max_input_tokens": 32000`, `"base_tier_max_input_tokens": -1`, 1)},
		{name: "trailing value", raw: validModelPricingJSON() + ` {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseModelPricingStrict([]byte(test.raw)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestModelPricingUpdateIsAtomicAndImmutable(t *testing.T) {
	service, err := NewStatic(mustParseConfig(t, validJSON()))
	if err != nil {
		t.Fatalf("NewStatic: %v", err)
	}
	first, err := service.ApplyModelPricingJSON([]byte(validModelPricingJSON()))
	if err != nil {
		t.Fatalf("ApplyModelPricingJSON: %v", err)
	}
	first.Models["qwen3.8-max"] = first.Models["qwen3-max"]
	current := service.ModelPricing()
	if current.Generation != 1 || current.Models["qwen3.8-max"].Input != 1.768842 {
		t.Fatalf("service-owned snapshot was mutated: %#v", current)
	}

	retained, err := service.ApplyModelPricingJSON([]byte(`{"version":1,"currency":"USD","unit":"per_million_tokens","models":{}}`))
	if err == nil {
		t.Fatal("expected invalid update error")
	}
	if retained.Generation != current.Generation || retained.SHA256 != current.SHA256 {
		t.Fatalf("invalid update replaced snapshot: %#v", retained)
	}

	updated := strings.Replace(validModelPricingJSON(), `"input": 1.768842`, `"input": 2`, 1)
	next, err := service.ApplyModelPricingJSON([]byte(updated))
	if err != nil {
		t.Fatalf("apply valid update: %v", err)
	}
	if next.Generation != 2 || next.SHA256 == current.SHA256 || next.Models["qwen3.8-max"].Input != 2 {
		t.Fatalf("updated snapshot = %#v", next)
	}
}

func TestRuntimeAndModelPricingUpdatesShareApplyLock(t *testing.T) {
	service, err := NewStatic(mustParseConfig(t, validJSON()))
	if err != nil {
		t.Fatalf("NewStatic: %v", err)
	}

	// Holding the runtime apply lock must also stop a pricing update. Otherwise
	// the two listeners can validate against stale opposite snapshots and commit
	// a runtime model list that its final pricing catalog does not cover.
	service.applyMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, applyErr := service.ApplyModelPricingJSON([]byte(validModelPricingJSON()))
		done <- applyErr
	}()

	select {
	case applyErr := <-done:
		service.applyMu.Unlock()
		t.Fatalf("pricing update bypassed runtime apply lock: %v", applyErr)
	case <-time.After(100 * time.Millisecond):
	}
	service.applyMu.Unlock()

	select {
	case applyErr := <-done:
		if applyErr != nil {
			t.Fatalf("pricing update after unlock: %v", applyErr)
		}
	case <-time.After(time.Second):
		t.Fatal("pricing update remained blocked after runtime apply lock was released")
	}
}

func TestDiamondModelPricingUpdatesBeforeManagedModelCatalog(t *testing.T) {
	client := &fakeDiamondClient{content: validJSON()}
	service, err := newDiamondService(nil, true, func() (diamondClient, error) { return client, nil })
	if err != nil {
		t.Fatalf("newDiamondService: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	if service.ModelPricing().Generation != 1 || client.modelPricingOnChange == nil {
		t.Fatalf("pricing snapshot=%#v listener=%v", service.ModelPricing(), client.modelPricingOnChange != nil)
	}

	runtimeWithFuture := strings.Replace(validJSON(),
		`"models": ["qwen3.8-max", "qwen3-max"]`,
		`"models": ["qwen3.8-max", "qwen3-max", "future-model"]`, 1)
	before := service.Current()
	client.onChange(runtimeWithFuture)
	if retained := service.Current(); retained.Generation != before.Generation || retained.SHA256 != before.SHA256 {
		t.Fatalf("runtime accepted an unpriced model: %#v", retained)
	}

	pricingWithFuture := strings.Replace(validModelPricingJSON(),
		`"qwen3-max": {`,
		`"future-model": {"input":1,"output":2,"cache_read":0.1,"cache_write":1}, "qwen3-max": {`, 1)
	client.modelPricingOnChange(pricingWithFuture)
	if _, ok := service.ModelPricing().Models["future-model"]; !ok {
		t.Fatalf("price-first update was not installed: %#v", service.ModelPricing())
	}
	client.onChange(runtimeWithFuture)
	if got := service.Current(); got.Generation != before.Generation+1 || !strings.Contains(strings.Join(got.Config.Runtime.LLM.Models, ","), "future-model") {
		t.Fatalf("priced runtime update was not installed: %#v", got)
	}
}

func TestModelPricingRequiresEveryManagedModel(t *testing.T) {
	cfg, err := ParseModelPricingStrict([]byte(validModelPricingJSON()))
	if err != nil {
		t.Fatalf("ParseModelPricingStrict: %v", err)
	}
	if err := cfg.ValidateModels([]string{"qwen3.8-max", "missing"}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error = %v, want missing model", err)
	}
}

func TestModelPricingLogsContainOnlySnapshotMetadata(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logModelPricingUpdate(logger, "updated", ModelPricingSnapshot{
		Version: 1, Currency: "USD", Unit: "per_million_tokens",
		Models:     map[string]modelpricing.Rate{"secret-model": {Input: 99}},
		Generation: 7, SHA256: "safe-hash",
	})
	logged := output.String()
	for _, forbidden := range []string{"secret-model", `"input":99`, `"models"`} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("log leaked pricing content %q: %s", forbidden, logged)
		}
	}
	for _, required := range []string{ModelPricingDiamondDataID, `"generation":7`, `"sha256":"safe-hash"`, `"count":1`} {
		if !strings.Contains(logged, required) {
			t.Fatalf("log missing %q: %s", required, logged)
		}
	}
}
