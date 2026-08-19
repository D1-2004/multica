package agentmessagerouter

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func scopeDetail(direct, group []string) *DingTalkMessageScopeDetail {
	return &DingTalkMessageScopeDetail{DirectCids: direct, GroupCids: group}
}

func scopeDetailWithEmoji(direct, group, emoji []string) *DingTalkMessageScopeDetail {
	return &DingTalkMessageScopeDetail{DirectCids: direct, GroupCids: group, EmojiReactionCids: emoji}
}

func TestNormalizeReportedMessageScope(t *testing.T) {
	t.Run("accepts", func(t *testing.T) {
		for _, tc := range []struct {
			name          string
			version       int
			detail        *DingTalkMessageScopeDetail
			require       bool
			wantVersion   int
			wantDetailNil bool
		}{
			{"missing version is legacy", 0, nil, true, DingTalkMessageScopeVersionLegacy, true},
			{"explicit legacy", 1, nil, true, DingTalkMessageScopeVersionLegacy, true},
			{"buckets without detail on failure falls back to legacy", 2, nil, false, DingTalkMessageScopeVersionLegacy, true},
			{"buckets with wildcard buckets", 2, scopeDetail([]string{"*"}, []string{"*"}), true, DingTalkMessageScopeVersionBuckets, false},
			{"buckets with mixed specified", 2, scopeDetail([]string{"101:202"}, []string{"grp-1"}), true, DingTalkMessageScopeVersionBuckets, false},
			{"buckets with one empty bucket", 2, scopeDetail([]string{}, []string{"grp-1"}), true, DingTalkMessageScopeVersionBuckets, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				version, detail, err := normalizeReportedMessageScope(tc.version, tc.detail, tc.require)
				if err != nil {
					t.Fatalf("normalizeReportedMessageScope: %v", err)
				}
				if version != tc.wantVersion || (detail == nil) != tc.wantDetailNil {
					t.Fatalf("version=%d detail=%#v", version, detail)
				}
			})
		}
	})
	t.Run("rejects", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			version int
			detail  *DingTalkMessageScopeDetail
			require bool
		}{
			{"unknown version", 3, nil, true},
			{"legacy must not carry detail", 1, scopeDetail([]string{"*"}, []string{"*"}), true},
			{"buckets success requires detail", 2, nil, true},
			{"detail requires both buckets", 2, &DingTalkMessageScopeDetail{DirectCids: []string{"*"}}, true},
			{"buckets must not both be empty", 2, scopeDetail([]string{}, []string{}), true},
			{"wildcard must be exclusive", 2, scopeDetail([]string{"*", "101:202"}, []string{}), true},
			{"cid rejects whitespace", 2, scopeDetail([]string{"101: 202"}, []string{}), true},
			{"cid rejects empty", 2, scopeDetail([]string{""}, []string{}), true},
			{"cid rejects oversize", 2, scopeDetail([]string{strings.Repeat("a", maxScopeCIDBytes+1)}, []string{}), true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if _, _, err := normalizeReportedMessageScope(tc.version, tc.detail, tc.require); err == nil {
					t.Fatal("expected rejection")
				}
			})
		}
	})
}

func testBoundScopeConfig(scope string) DingTalkAccountConfig {
	boundAt := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	return DingTalkAccountConfig{
		SchemaVersion:      1,
		DispatchEndpointID: "v1_AAECAwQFBgcICQoLDA0ODw",
		DispatchKeyID:      "v1",
		RouterSourceID:     "source-channel",
		RouterPlatform:     "dingtalk",
		RouterTenantID:     "corp-a",
		RouterAccountID:    "employee-a",
		EnabledDomains:     []string{"channel"},
		MessageScope:       scope,
		BoundAt:            &boundAt,
	}
}

func messageRouteOutcome(config DingTalkAccountConfig) PublicDingTalkBindingOutcome {
	return config.PublicBinding("workspace-1", "agent-1", "active", PublicDingTalkBindingOutcome{Status: "unbound"}).MessageRoute
}

func TestPublicBindingUpgradesLegacyScopeViews(t *testing.T) {
	t.Run("all upgrades to double wildcard", func(t *testing.T) {
		outcome := messageRouteOutcome(testBoundScopeConfig(DingTalkMessageScopeAll))
		if outcome.MessageScopeVersion != DingTalkMessageScopeVersionLegacy {
			t.Fatalf("version = %d", outcome.MessageScopeVersion)
		}
		if len(outcome.Subscription.DirectCids) != 1 || outcome.Subscription.DirectCids[0] != "*" ||
			len(outcome.Subscription.GroupCids) != 1 || outcome.Subscription.GroupCids[0] != "*" {
			t.Fatalf("subscription = %#v", outcome.Subscription)
		}
		if outcome.LegacyView.MessageScope != "all" || len(outcome.LegacyView.Cids) != 1 || outcome.LegacyView.Cids[0] != "*" {
			t.Fatalf("legacy view = %#v", outcome.LegacyView)
		}
	})
	t.Run("direct_only upgrades to behavior-equivalent empty buckets", func(t *testing.T) {
		outcome := messageRouteOutcome(testBoundScopeConfig(DingTalkMessageScopeDirectOnly))
		if len(outcome.Subscription.DirectCids) != 0 || len(outcome.Subscription.GroupCids) != 0 {
			t.Fatalf("subscription = %#v", outcome.Subscription)
		}
		if outcome.LegacyView.MessageScope != "direct_only" ||
			len(outcome.LegacyView.Cids) != 1 || outcome.LegacyView.Cids[0] != "employee-a:employee-a" {
			t.Fatalf("legacy view = %#v", outcome.LegacyView)
		}
	})
	t.Run("custom splits conversations by cid colon", func(t *testing.T) {
		config := testBoundScopeConfig(DingTalkMessageScopeCustom)
		config.Conversations = []DingTalkConversationSnapshot{
			{CID: "101:202", Name: "Alice"},
			{CID: "grp-1", Name: "Project"},
		}
		outcome := messageRouteOutcome(config)
		if len(outcome.Subscription.DirectCids) != 1 || outcome.Subscription.DirectCids[0] != "101:202" ||
			len(outcome.Subscription.GroupCids) != 1 || outcome.Subscription.GroupCids[0] != "grp-1" {
			t.Fatalf("subscription = %#v", outcome.Subscription)
		}
		if outcome.LegacyView.MessageScope != "custom" || len(outcome.LegacyView.Cids) != 2 {
			t.Fatalf("legacy view = %#v", outcome.LegacyView)
		}
	})
}

func TestPublicBindingDowngradesBucketScopeViews(t *testing.T) {
	withDetail := func(detail *DingTalkMessageScopeDetail, scope string) DingTalkAccountConfig {
		config := testBoundScopeConfig(scope)
		config.MessageScopeVersion = DingTalkMessageScopeVersionBuckets
		config.MessageScopeDetail = detail
		return config
	}
	t.Run("double wildcard downgrades to all", func(t *testing.T) {
		outcome := messageRouteOutcome(withDetail(scopeDetail([]string{"*"}, []string{"*"}), DingTalkMessageScopeAll))
		if outcome.MessageScopeVersion != DingTalkMessageScopeVersionBuckets {
			t.Fatalf("version = %d", outcome.MessageScopeVersion)
		}
		if outcome.LegacyView.MessageScope != "all" || len(outcome.LegacyView.Cids) != 1 || outcome.LegacyView.Cids[0] != "*" {
			t.Fatalf("legacy view = %#v", outcome.LegacyView)
		}
	})
	t.Run("all direct plus no group downgrades to direct_only with synthesized self cid", func(t *testing.T) {
		outcome := messageRouteOutcome(withDetail(scopeDetail([]string{"*"}, []string{}), DingTalkMessageScopeDirectOnly))
		if outcome.LegacyView.MessageScope != "direct_only" ||
			len(outcome.LegacyView.Cids) != 1 || outcome.LegacyView.Cids[0] != "employee-a:employee-a" {
			t.Fatalf("legacy view = %#v", outcome.LegacyView)
		}
	})
	t.Run("specified only downgrades to merged custom cids", func(t *testing.T) {
		outcome := messageRouteOutcome(withDetail(scopeDetail([]string{"101:202"}, []string{"grp-1"}), DingTalkMessageScopeCustom))
		if outcome.LegacyView.MessageScope != "custom" ||
			len(outcome.LegacyView.Cids) != 2 || outcome.LegacyView.Cids[0] != "101:202" || outcome.LegacyView.Cids[1] != "grp-1" {
			t.Fatalf("legacy view = %#v", outcome.LegacyView)
		}
	})
	t.Run("one wildcard dimension is inexpressible in legacy view", func(t *testing.T) {
		outcome := messageRouteOutcome(withDetail(scopeDetail([]string{}, []string{"*"}), DingTalkMessageScopeCustom))
		if outcome.LegacyView.MessageScope != "custom" || outcome.LegacyView.Cids != nil {
			t.Fatalf("legacy view = %#v", outcome.LegacyView)
		}
		encoded, err := json.Marshal(outcome)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"cids":null`) {
			t.Fatalf("legacy cids should serialize as null: %s", encoded)
		}
	})
	t.Run("subscription view mirrors stored detail", func(t *testing.T) {
		outcome := messageRouteOutcome(withDetail(scopeDetail([]string{"101:202"}, []string{}), DingTalkMessageScopeCustom))
		if len(outcome.Subscription.DirectCids) != 1 || outcome.Subscription.DirectCids[0] != "101:202" ||
			outcome.Subscription.GroupCids == nil || len(outcome.Subscription.GroupCids) != 0 {
			t.Fatalf("subscription = %#v", outcome.Subscription)
		}
	})
}

func TestDingTalkAccountConfigMessageScopeVersionRoundTrip(t *testing.T) {
	config := testBoundScopeConfig(DingTalkMessageScopeCustom)
	config.Conversations = []DingTalkConversationSnapshot{{CID: "grp-1", Name: "Project"}}
	config.MessageScopeVersion = DingTalkMessageScopeVersionBuckets
	config.MessageScopeDetail = scopeDetail([]string{}, []string{"grp-1"})
	raw, err := config.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	parsed, err := ParseDingTalkAccountConfig(raw)
	if err != nil {
		t.Fatalf("ParseDingTalkAccountConfig: %v", err)
	}
	if parsed.MessageScopeVersion != DingTalkMessageScopeVersionBuckets ||
		parsed.MessageScopeDetail == nil || len(parsed.MessageScopeDetail.GroupCids) != 1 {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestDingTalkAccountConfigRelaxesCustomConversationsForBuckets(t *testing.T) {
	config := testBoundScopeConfig(DingTalkMessageScopeCustom)
	config.MessageScopeVersion = DingTalkMessageScopeVersionBuckets
	config.MessageScopeDetail = scopeDetail([]string{}, []string{"*"})
	if _, err := config.Marshal(); err != nil {
		t.Fatalf("v2 custom without conversations should marshal: %v", err)
	}

	legacy := testBoundScopeConfig(DingTalkMessageScopeCustom)
	if _, err := legacy.Marshal(); err == nil {
		t.Fatal("v1 custom without conversations was accepted")
	}
}

func TestDingTalkAccountConfigRejectsMismatchedScopeVersion(t *testing.T) {
	legacyWithDetail := testBoundScopeConfig(DingTalkMessageScopeAll)
	legacyWithDetail.MessageScopeDetail = scopeDetail([]string{"*"}, []string{"*"})
	if _, err := legacyWithDetail.Marshal(); err == nil {
		t.Fatal("v1 config carrying detail was accepted")
	}

	bucketsWithoutDetail := testBoundScopeConfig(DingTalkMessageScopeAll)
	bucketsWithoutDetail.MessageScopeVersion = DingTalkMessageScopeVersionBuckets
	if _, err := bucketsWithoutDetail.Marshal(); err == nil {
		t.Fatal("v2 config without detail was accepted")
	}
}

func TestDingTalkAccountConfigDefaultsMissingScopeVersionToLegacy(t *testing.T) {
	raw := []byte(`{
		"schema_version":1,
		"dispatch_endpoint_id":"v1_AAECAwQFBgcICQoLDA0ODw",
		"dispatch_key_id":"v1",
		"message_scope":"all"
	}`)
	config, err := ParseDingTalkAccountConfig(raw)
	if err != nil {
		t.Fatalf("ParseDingTalkAccountConfig: %v", err)
	}
	if config.MessageScopeVersion != DingTalkMessageScopeVersionLegacy || config.MessageScopeDetail != nil {
		t.Fatalf("parsed = %#v", config)
	}
}

func TestNormalizeMessageScopeDetailEmojiReactionBucket(t *testing.T) {
	t.Run("missing emoji bucket normalizes to empty", func(t *testing.T) {
		detail, err := normalizeDingTalkMessageScopeDetail(scopeDetail([]string{}, []string{"grp-1"}))
		if err != nil {
			t.Fatalf("normalizeDingTalkMessageScopeDetail: %v", err)
		}
		if detail.EmojiReactionCids == nil || len(detail.EmojiReactionCids) != 0 {
			t.Fatalf("emoji bucket = %#v", detail.EmojiReactionCids)
		}
	})
	t.Run("accepts specified emoji cids", func(t *testing.T) {
		detail, err := normalizeDingTalkMessageScopeDetail(
			scopeDetailWithEmoji([]string{"101:202"}, []string{}, []string{"grp-1", "grp-2"}))
		if err != nil {
			t.Fatalf("normalizeDingTalkMessageScopeDetail: %v", err)
		}
		if len(detail.EmojiReactionCids) != 2 || detail.EmojiReactionCids[0] != "grp-1" {
			t.Fatalf("emoji bucket = %#v", detail.EmojiReactionCids)
		}
	})
	t.Run("rejects", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			detail *DingTalkMessageScopeDetail
		}{
			{"emoji bucket rejects wildcard", scopeDetailWithEmoji([]string{"*"}, []string{}, []string{"*"})},
			{"emoji bucket rejects wildcard among cids", scopeDetailWithEmoji([]string{"*"}, []string{}, []string{"grp-1", "*"})},
			{"emoji bucket rejects empty cid", scopeDetailWithEmoji([]string{"*"}, []string{}, []string{""})},
			{"emoji bucket rejects whitespace cid", scopeDetailWithEmoji([]string{"*"}, []string{}, []string{"grp 1"})},
			{"emoji bucket rejects oversize cid", scopeDetailWithEmoji([]string{"*"}, []string{}, []string{strings.Repeat("a", maxScopeCIDBytes+1)})},
			{"emoji bucket does not satisfy the both-empty rule", scopeDetailWithEmoji([]string{}, []string{}, []string{"grp-1"})},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := normalizeDingTalkMessageScopeDetail(tc.detail); err == nil {
					t.Fatal("expected rejection")
				}
			})
		}
	})
}

func TestPublicBindingEchoesEmojiReactionScope(t *testing.T) {
	t.Run("v1 upgrades to empty emoji bucket", func(t *testing.T) {
		outcome := messageRouteOutcome(testBoundScopeConfig(DingTalkMessageScopeAll))
		if outcome.Subscription.EmojiReactionCids == nil || len(outcome.Subscription.EmojiReactionCids) != 0 {
			t.Fatalf("emoji view = %#v", outcome.Subscription.EmojiReactionCids)
		}
		if len(outcome.EmojiConversations) != 0 {
			t.Fatalf("emoji conversations = %#v", outcome.EmojiConversations)
		}
	})
	t.Run("v2 echoes stored emoji bucket and conversations", func(t *testing.T) {
		config := testBoundScopeConfig(DingTalkMessageScopeCustom)
		config.MessageScopeVersion = DingTalkMessageScopeVersionBuckets
		config.MessageScopeDetail = scopeDetailWithEmoji([]string{}, []string{"grp-1"}, []string{"grp-2"})
		config.Conversations = []DingTalkConversationSnapshot{{CID: "grp-1", Name: "Project"}}
		config.EmojiConversations = []DingTalkConversationSnapshot{{CID: "grp-2", Name: "Emoji Group"}}
		outcome := messageRouteOutcome(config)
		if len(outcome.Subscription.EmojiReactionCids) != 1 || outcome.Subscription.EmojiReactionCids[0] != "grp-2" {
			t.Fatalf("emoji view = %#v", outcome.Subscription.EmojiReactionCids)
		}
		if len(outcome.EmojiConversations) != 1 || outcome.EmojiConversations[0].CID != "grp-2" {
			t.Fatalf("emoji conversations = %#v", outcome.EmojiConversations)
		}
	})
}

func TestDingTalkAccountConfigEmojiFieldsRoundTrip(t *testing.T) {
	config := testBoundScopeConfig(DingTalkMessageScopeCustom)
	config.MessageScopeVersion = DingTalkMessageScopeVersionBuckets
	config.MessageScopeDetail = scopeDetailWithEmoji([]string{}, []string{"grp-1"}, []string{"grp-2"})
	config.Conversations = []DingTalkConversationSnapshot{{CID: "grp-1", Name: "Project"}}
	config.EmojiConversations = []DingTalkConversationSnapshot{{CID: "grp-2", Name: "Emoji Group"}}
	raw, err := config.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	parsed, err := ParseDingTalkAccountConfig(raw)
	if err != nil {
		t.Fatalf("ParseDingTalkAccountConfig: %v", err)
	}
	if parsed.MessageScopeDetail == nil ||
		len(parsed.MessageScopeDetail.EmojiReactionCids) != 1 ||
		parsed.MessageScopeDetail.EmojiReactionCids[0] != "grp-2" {
		t.Fatalf("parsed detail = %#v", parsed.MessageScopeDetail)
	}
	if len(parsed.EmojiConversations) != 1 || parsed.EmojiConversations[0].CID != "grp-2" {
		t.Fatalf("parsed emoji conversations = %#v", parsed.EmojiConversations)
	}
}
