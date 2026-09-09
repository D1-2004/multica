// Package coordinatorcontract defines the bounded, authored routing contract.
// Contracts may narrow platform actions; they never introduce tools or expand
// execution permissions. Full job instructions remain executor-owned.
package coordinatorcontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	Version            = 1
	MaxCharacters      = 1600
	StateLoaded        = "loaded"
	StateNotConfigured = "not_configured"
	StateStale         = "stale"
	StateUnavailable   = "unavailable"
)

type Contract struct {
	Version      int      `json:"version" yaml:"version"`
	Scope        string   `json:"scope" yaml:"scope"`
	MustDelegate []string `json:"must_delegate" yaml:"must_delegate"`
	Constraints  []string `json:"constraints" yaml:"constraints"`
	ClarifyWhen  []string `json:"clarify_when" yaml:"clarify_when"`
	// SourceInstructionsSHA256 records the Host-bound instruction version.
	// Copies retain it; it is never an authorization credential.
	SourceInstructionsSHA256 string `json:"source_instructions_sha256,omitempty" yaml:"source_instructions_sha256,omitempty"`
}

// Parse validates the whole object without truncating or summarizing content.
// Empty SQL values and JSON null mean no authored contract.
func Parse(raw []byte) (*Contract, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var contract Contract
	if err := decoder.Decode(&contract); err != nil {
		return nil, fmt.Errorf("coordinator_contract: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("coordinator_contract must contain one JSON object")
	}
	if contract.MustDelegate == nil {
		contract.MustDelegate = []string{}
	}
	if contract.Constraints == nil {
		contract.Constraints = []string{}
	}
	if contract.ClarifyWhen == nil {
		contract.ClarifyWhen = []string{}
	}
	if err := validate(&contract); err != nil {
		return nil, err
	}
	return &contract, nil
}

// Bind copies an authored contract and binds it to the exact job instructions.
// A client-provided source hash is always replaced. The bound object, including
// its hash and JSON structure, must fit the total context budget.
func Bind(contract *Contract, instructions string) (*Contract, error) {
	if contract == nil {
		return nil, nil
	}
	bound := *contract
	bound.MustDelegate = append([]string{}, contract.MustDelegate...)
	bound.Constraints = append([]string{}, contract.Constraints...)
	bound.ClarifyWhen = append([]string{}, contract.ClarifyWhen...)
	bound.SourceInstructionsSHA256 = instructionsHash(instructions)
	if err := validate(&bound); err != nil {
		return nil, err
	}
	return &bound, nil
}

// Resolve distinguishes an absent contract from invalid or outdated constraints.
// Callers must use the state, not the pointer's presence, to decide whether the
// short contract may replace a full-instruction review.
func Resolve(raw []byte, instructions string) (*Contract, string) {
	contract, err := Parse(raw)
	if err != nil {
		return nil, StateUnavailable
	}
	if contract == nil {
		return nil, StateNotConfigured
	}
	if contract.SourceInstructionsSHA256 != instructionsHash(instructions) {
		return contract, StateStale
	}
	return contract, StateLoaded
}

// Marshal returns canonical JSON, or nil for SQL NULL. Contract has no values
// json.Marshal can reject; public writes must first use Parse and Bind.
func Marshal(contract *Contract) []byte {
	if contract == nil {
		return nil
	}
	raw, _ := json.Marshal(contract)
	return raw
}

func Hash(contract *Contract) string {
	if contract == nil {
		return ""
	}
	sum := sha256.Sum256(Marshal(contract))
	return hex.EncodeToString(sum[:])
}

func instructionsHash(instructions string) string {
	sum := sha256.Sum256([]byte(instructions))
	return hex.EncodeToString(sum[:])
}

func validate(contract *Contract) error {
	if contract.Version != Version {
		return fmt.Errorf("coordinator_contract.version must be %d", Version)
	}
	if strings.TrimSpace(contract.Scope) == "" {
		return errors.New("coordinator_contract.scope must be non-empty")
	}
	for name, entries := range map[string][]string{"must_delegate": contract.MustDelegate, "constraints": contract.Constraints, "clarify_when": contract.ClarifyWhen} {
		for _, entry := range entries {
			if strings.TrimSpace(entry) == "" {
				return fmt.Errorf("coordinator_contract.%s cannot contain empty entries", name)
			}
		}
	}
	if contract.SourceInstructionsSHA256 != "" {
		decoded, err := hex.DecodeString(contract.SourceInstructionsSHA256)
		if err != nil || len(decoded) != sha256.Size || strings.ToLower(contract.SourceInstructionsSHA256) != contract.SourceInstructionsSHA256 {
			return errors.New("coordinator_contract.source_instructions_sha256 must be a lowercase SHA-256 hash")
		}
	}
	if utf8.RuneCount(Marshal(contract)) > MaxCharacters {
		return fmt.Errorf("coordinator_contract must be %d characters or fewer including JSON fields and source hash", MaxCharacters)
	}
	return nil
}
