package runtimeconfig

import (
	"encoding/json"
	"testing"
)

func TestAgenticFSQuotaValidation(t *testing.T) {
	cfg, err := ParseStrict([]byte(validJSON()), true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime.AgenticFS != (AgenticFSConfig{SizeLimit: 100 << 30, FileCountLimit: 1000000000}) {
		t.Fatal("missing section must use managed defaults")
	}
	for _, test := range []struct {
		name  string
		quota AgenticFSConfig
		valid bool
	}{
		{"maximum file count", AgenticFSConfig{SizeLimit: 100 << 30, FileCountLimit: 1000000000}, true},
		{"over file maximum", AgenticFSConfig{SizeLimit: 100 << 30, FileCountLimit: 1000000001}, false},
		{"below minimum", AgenticFSConfig{SizeLimit: 100 << 30, FileCountLimit: 9999}, false},
		{"negative size", AgenticFSConfig{SizeLimit: -1, FileCountLimit: 1000000000}, false},
		{"fractional GiB", AgenticFSConfig{SizeLimit: 100<<30 + 1, FileCountLimit: 1000000000}, false},
		{"partial config", AgenticFSConfig{SizeLimit: 100 << 30, FileCountLimit: 0}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg.Runtime.AgenticFS = test.quota
			raw, _ := json.Marshal(cfg)
			_, err := ParseStrict(raw, true)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v, error=%v", test.valid, err)
			}
		})
	}
}
