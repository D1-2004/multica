package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

func main() {
	file := flag.String("file", "", "path to a complete runtime Diamond JSON document")
	production := flag.Bool("production", false, "apply production-only validation rules")
	flag.Parse()
	if *file == "" {
		fmt.Fprintln(os.Stderr, "-file is required")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read runtime config: %v\n", err)
		os.Exit(1)
	}
	cfg, err := runtimeconfig.ParseStrict(raw, *production)
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate runtime config: %v\n", err)
		os.Exit(1)
	}
	sum := sha256.Sum256(raw)
	result := struct {
		SchemaVersion int    `json:"schema_version"`
		SHA256        string `json:"sha256"`
		Models        int    `json:"models"`
		DefaultModel  string `json:"default_model"`
	}{
		SchemaVersion: cfg.Version,
		SHA256:        hex.EncodeToString(sum[:]),
		Models:        len(cfg.Runtime.LLM.Models),
		DefaultModel:  cfg.Runtime.LLM.DefaultModel,
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "encode validation result: %v\n", err)
		os.Exit(1)
	}
}
