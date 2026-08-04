package featureflag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sync/atomic"
)

const (
	DispatchCommonRuntimePromptFlagKey = "dispatch_common_runtime_prompt"
	DispatchIssueRuntimePromptFlagKey  = "dispatch_issue_runtime_prompt"
	DispatchChatRuntimePromptFlagKey   = "dispatch_chat_runtime_prompt"
	DispatchAutoRuntimePromptFlagKey   = "dispatch_auto_runtime_prompt"
)

type diamondSnapshot struct {
	rules  map[string]Rule
	keys   []string
	digest string
}

// DiamondProvider is an immutable-snapshot Provider updated by Diamond.
// A new body is parsed and validated in full before the pointer swap, so
// readers observe either the previous snapshot or the complete new snapshot.
type DiamondProvider struct {
	snapshot atomic.Pointer[diamondSnapshot]
}

func NewDiamondProvider() *DiamondProvider {
	provider := &DiamondProvider{}
	provider.snapshot.Store(&diamondSnapshot{rules: map[string]Rule{}, keys: []string{}, digest: emptySHA256()})
	return provider
}

func (*DiamondProvider) Name() string { return "diamond" }

func (p *DiamondProvider) Lookup(ctx context.Context, key string) (Decision, bool) {
	if p == nil {
		return Decision{}, false
	}
	snapshot := p.snapshot.Load()
	if snapshot == nil {
		return Decision{}, false
	}
	rule, found := snapshot.rules[key]
	if !found {
		return Decision{}, false
	}
	decision := evaluateRule(key, rule, EvalContextFrom(ctx))
	decision.Source = p.Name()
	return decision, true
}

func (p *DiamondProvider) ApplyJSON(data []byte) (int, string, error) {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	rules, err := parseDiamondPromptsJSON(data)
	if err != nil {
		count, _ := p.SnapshotMetadata()
		return count, digest, err
	}
	keys := make([]string, 0, len(rules))
	for key := range rules {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	p.snapshot.Store(&diamondSnapshot{rules: rules, keys: keys, digest: digest})
	return len(rules), digest, nil
}

func (p *DiamondProvider) SnapshotMetadata() (int, string) {
	if p == nil {
		return 0, ""
	}
	snapshot := p.snapshot.Load()
	if snapshot == nil {
		return 0, ""
	}
	return len(snapshot.rules), snapshot.digest
}

func (p *DiamondProvider) Keys() []string {
	if p == nil {
		return nil
	}
	snapshot := p.snapshot.Load()
	if snapshot == nil {
		return nil
	}
	return slices.Clone(snapshot.keys)
}
