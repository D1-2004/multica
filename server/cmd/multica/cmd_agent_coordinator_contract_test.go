package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestCoordinatorContractCLI(t *testing.T) {
	fresh := func() *cobra.Command { cmd := &cobra.Command{}; registerCoordinatorContractFlags(cmd); return cmd }
	cmd := fresh()
	if _, present, err := resolveCoordinatorContract(cmd); present || err != nil {
		t.Fatalf("omitted=(%v,%v)", present, err)
	}
	path := filepath.Join(t.TempDir(), "contract.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"scope":"查证","must_delegate":[],"constraints":[],"clarify_when":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("coordinator-contract-file", path); err != nil {
		t.Fatal(err)
	}
	raw, present, err := resolveCoordinatorContract(cmd)
	if err != nil || !present || !json.Valid(raw) {
		t.Fatalf("file=(%s,%v,%v)", raw, present, err)
	}
	for _, value := range []string{"null", `{"version":2,"scope":"x"}`, `{"version":1,"scope":"x","reply":"escape"}`, ""} {
		cmd = fresh()
		_ = cmd.Flags().Set("coordinator-contract", value)
		raw, present, err = resolveCoordinatorContract(cmd)
		if value == "null" {
			if err != nil || !present || string(raw) != "null" {
				t.Fatalf("clear=(%s,%v,%v)", raw, present, err)
			}
		} else if err == nil {
			t.Fatalf("invalid value %q accepted", value)
		}
	}
}
