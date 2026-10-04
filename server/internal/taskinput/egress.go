package taskinput

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/pkg/redact"
)

// Audience is the egress tier of an outgoing invitation: who can read the
// exact bytes once they are sent.
type Audience string

const (
	// AudienceDirect: a 1:1 conversation with the invited participant only.
	AudienceDirect Audience = "direct"
	// AudienceGroup: every present member and anyone who joins later. The
	// least-trusted reader decides what may be included.
	AudienceGroup Audience = "group"
)

func audienceRank(a Audience) int {
	switch a {
	case AudienceDirect:
		return 1
	case AudienceGroup:
		return 2
	default:
		return 0
	}
}

// AudienceForSceneKind maps a directory scene kind to its egress tier.
func AudienceForSceneKind(kind string) (Audience, bool) {
	switch kind {
	case sceneKindDM:
		return AudienceDirect, true
	case sceneKindGroup:
		return AudienceGroup, true
	default:
		return "", false
	}
}

// PrivateFragment is a piece of private context the renderer could have
// leaked: origin conversation text, the requester's private memory, another
// participant's answer or question. MaxAudience is the widest tier allowed to
// see it; empty means no invitation audience at all.
type PrivateFragment struct {
	Text        string   `json:"-"`
	MaxAudience Audience `json:"max_audience,omitempty"`
}

// minScannableRunes: shorter fragments ("7", "ok") would hold almost any
// text, so they cannot be enforced by a byte scan and are skipped. Keeping
// them out is the renderer's job: questions come only from fields the
// requester authorized plus the Host template.
const minScannableRunes = 4

// MaxQuestionRunes bounds an invitation question.
const MaxQuestionRunes = 2000

// maxRenderedBytes bounds the rendered invitation (question plus template).
const maxRenderedBytes = 16 * 1024

type EgressInput struct {
	// Rendered is the exact text that will be sent, after all templating.
	Rendered string
	// Audience is the tier the caller believes it is sending to.
	Audience Audience
	// TargetSceneKind is the directory kind of the target scene; it must agree
	// with Audience, or the send is held.
	TargetSceneKind string
	Private         []PrivateFragment
}

// Egress hold reasons.
const (
	HoldEmpty            = "empty"
	HoldTooLong          = "too_long"
	HoldAudienceMismatch = "audience_mismatch"
	HoldSecret           = "secret"
	HoldConfigLink       = "config_link"
	HoldPrivateFragment  = "private_fragment"
)

// EgressResult says whether the rendered bytes may leave. RenderedHash binds
// the delivery record (RecordDeliveryParams.RenderedHash) to the bytes that
// were scanned. A held result must not be sent in any partially scrubbed form.
type EgressResult struct {
	Held         bool     `json:"held"`
	Reasons      []string `json:"reasons,omitempty"`
	RenderedHash string   `json:"rendered_hash"`
}

// configLinkPattern matches a context configuration bearer link, plain or
// percent-encoded once or twice (see inboundcoord.RedactConfigLinks).
var configLinkPattern = regexp.MustCompile(`(?i)configure(?:\?|%3f|%253f)link(?:=|%3d|%253d)`)

// CheckEgress runs the final scan over the exact bytes of an invitation
// before they leave, following GawkBot's packer: classify by the least-trusted
// reader, scan what actually leaves rather than its inputs, and hold the whole
// message on any failure.
func CheckEgress(in EgressInput) EgressResult {
	sum := sha256.Sum256([]byte(in.Rendered))
	result := EgressResult{RenderedHash: hex.EncodeToString(sum[:])}
	hold := func(reason string) {
		result.Held = true
		for _, r := range result.Reasons {
			if r == reason {
				return
			}
		}
		result.Reasons = append(result.Reasons, reason)
	}
	if strings.TrimSpace(in.Rendered) == "" {
		hold(HoldEmpty)
	}
	if len(in.Rendered) > maxRenderedBytes || !utf8.ValidString(in.Rendered) {
		hold(HoldTooLong)
	}
	expected, ok := AudienceForSceneKind(in.TargetSceneKind)
	if !ok || expected != in.Audience {
		hold(HoldAudienceMismatch)
	}
	if redact.Text(in.Rendered) != in.Rendered {
		hold(HoldSecret)
	}
	if configLinkPattern.MatchString(in.Rendered) {
		hold(HoldConfigLink)
	}
	rank := audienceRank(in.Audience)
	for _, fragment := range in.Private {
		text := strings.TrimSpace(fragment.Text)
		if utf8.RuneCountInString(text) < minScannableRunes {
			continue
		}
		allowed := fragment.MaxAudience != "" && rank > 0 && rank <= audienceRank(fragment.MaxAudience)
		if !allowed && strings.Contains(in.Rendered, text) {
			hold(HoldPrivateFragment)
		}
	}
	return result
}
