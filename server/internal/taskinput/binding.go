package taskinput

import "strings"

// MaxReplyHops bounds the reply/quote chain walk, as GawkBot's
// UltimateThreadRoot does, so cycles or pathological chains cannot loop.
const MaxReplyHops = 8

// InboundMessage is the Host's normalized view of one inbound message in a
// directory scene. SenderRef comes from the trusted identity bridge and
// SenderKind/MessageKind from the provider envelope, never from body text.
type InboundMessage struct {
	MessageID   string      `json:"message_id"`
	SceneID     string      `json:"scene_id"`
	SceneKind   string      `json:"scene_kind"`
	SenderRef   string      `json:"sender_ref"`
	SenderKind  SenderKind  `json:"sender_kind"`
	MessageKind MessageKind `json:"message_kind"`
	// ReplyToID is the provider reply-to / quoted message id, if any.
	ReplyToID string `json:"reply_to_id,omitempty"`
	// InviteRefs are invitation ids the Host resolved from an explicit,
	// structured reference (for example a clarification choice it offered to
	// this sender). Free text never becomes a reference by itself.
	InviteRefs []string `json:"invite_refs,omitempty"`
}

type BindOutcome string

const (
	// BindBound: the message answers exactly one of the sender's invitations.
	BindBound BindOutcome = "bound"
	// BindIgnored: the message can never be an answer (self, bot, card,
	// system notice, a group message without a reply).
	BindIgnored BindOutcome = "ignored"
	// BindUnbound: the message is not an answer to any open invitation; it is
	// ordinary conversation for the scene's own Loop.
	BindUnbound BindOutcome = "unbound"
	// BindAmbiguous: several of the sender's invitations fit. The Loop asks
	// the sender which question they answered; it never picks the latest.
	BindAmbiguous BindOutcome = "ambiguous"
)

// Binding is the result of BindAnswer. Candidates are only the sender's own
// invitations, so showing their questions back to the sender leaks nothing.
type Binding struct {
	Outcome      BindOutcome        `json:"outcome"`
	Kind         BindingKind        `json:"kind,omitempty"`
	InvitationID string             `json:"invitation_id,omitempty"`
	Reason       string             `json:"reason,omitempty"`
	Candidates   []BindingCandidate `json:"candidates,omitempty"`
}

// Binding reasons.
const (
	ReasonSelfMessage          = "self_message"
	ReasonBotMessage           = "bot_message"
	ReasonUnknownSender        = "unknown_sender"
	ReasonNotAnswerContent     = "not_answer_content"
	ReasonUnidentifiedSender   = "unidentified_sender"
	ReasonUnsupportedScene     = "unsupported_scene"
	ReasonGroupWithoutReply    = "group_without_reply"
	ReasonReplyNotInvitation   = "reply_not_invitation"
	ReasonReplyChainTooLong    = "reply_chain_too_long"
	ReasonInviteRefUnknown     = "invite_reference_unknown"
	ReasonNoPendingInvitation  = "no_pending_invitation"
	ReasonSeveralPending       = "several_pending_invitations"
	ReasonSeveralReferences    = "several_invite_references"
	ReasonDuplicateProviderMsg = "duplicate_invitation_message"
)

// ReplyChain walks the reply-to chain from start: start itself, its parent,
// and so on, visiting at most MaxReplyHops messages. parents maps a message id
// to the id it replied to or quoted, from the scene's stored history. The walk
// stops at a message without a known parent, before revisiting a message
// (cycle), or at the hop limit; complete is false only when the hop limit cut
// off a chain that continued.
func ReplyChain(start string, parents map[string]string) (chain []string, complete bool) {
	current := strings.TrimSpace(start)
	seen := make(map[string]struct{}, MaxReplyHops)
	for current != "" {
		if _, loop := seen[current]; loop {
			return chain, true
		}
		if len(chain) == MaxReplyHops {
			return chain, false
		}
		seen[current] = struct{}{}
		chain = append(chain, current)
		current = strings.TrimSpace(parents[current])
	}
	return chain, true
}

// BindAnswer decides whether msg answers one of the sender's invitations.
// candidates are the open invitations the store returned for msg.SenderRef
// (BindingCandidates); entries for other participants or scenes are ignored.
//
// Rules, strongest first:
//   - Messages from this Agent, from any bot, from an unidentified sender, and
//     cards or system notices (including a digital employee's auto-posted
//     intro card) are never answers. A digital employee's own text messages
//     answer like a person's when it was explicitly invited.
//   - A reply/quote chain that reaches the sender's invitation message within
//     MaxReplyHops binds strongly. A reply that leads elsewhere is not an
//     answer; it never falls back to a guess.
//   - In a group, a message without a reply is never an answer.
//   - In a 1:1 scene, an explicit invitation reference binds when it names
//     exactly one of the sender's invitations there; without one, the message
//     binds only when the sender has exactly one delivered, unanswered
//     invitation in that scene. Several fit means ambiguous, never "latest".
func BindAnswer(msg InboundMessage, parents map[string]string, candidates []BindingCandidate) Binding {
	switch msg.SenderKind {
	case SenderSelf:
		return Binding{Outcome: BindIgnored, Reason: ReasonSelfMessage}
	case SenderBot:
		return Binding{Outcome: BindIgnored, Reason: ReasonBotMessage}
	}
	if !msg.SenderKind.Answerer() {
		return Binding{Outcome: BindIgnored, Reason: ReasonUnknownSender}
	}
	if !msg.MessageKind.Answerable() {
		return Binding{Outcome: BindIgnored, Reason: ReasonNotAnswerContent}
	}
	sender := strings.TrimSpace(msg.SenderRef)
	if sender == "" || sender != msg.SenderRef {
		return Binding{Outcome: BindIgnored, Reason: ReasonUnidentifiedSender}
	}
	if msg.SceneKind != sceneKindDM && msg.SceneKind != sceneKindGroup {
		return Binding{Outcome: BindIgnored, Reason: ReasonUnsupportedScene}
	}
	own := make([]BindingCandidate, 0, len(candidates))
	for _, c := range candidates {
		if c.ParticipantRef == sender && c.TargetSceneID == msg.SceneID && (c.State == InvitationDelivered || c.State == InvitationAnswered) {
			own = append(own, c)
		}
	}

	if strings.TrimSpace(msg.ReplyToID) != "" {
		chain, complete := ReplyChain(msg.ReplyToID, parents)
		for _, node := range chain {
			var matches []BindingCandidate
			for _, c := range own {
				if c.ProviderMessageID != "" && c.ProviderMessageID == node {
					matches = append(matches, c)
				}
			}
			switch len(matches) {
			case 0:
				continue
			case 1:
				return Binding{Outcome: BindBound, Kind: BindReplyChain, InvitationID: matches[0].InvitationID}
			default:
				return Binding{Outcome: BindAmbiguous, Reason: ReasonDuplicateProviderMsg, Candidates: matches}
			}
		}
		if !complete {
			return Binding{Outcome: BindUnbound, Reason: ReasonReplyChainTooLong}
		}
		return Binding{Outcome: BindUnbound, Reason: ReasonReplyNotInvitation}
	}

	if msg.SceneKind == sceneKindGroup {
		return Binding{Outcome: BindIgnored, Reason: ReasonGroupWithoutReply}
	}

	if len(msg.InviteRefs) > 0 {
		refs := make(map[string]struct{}, len(msg.InviteRefs))
		for _, ref := range msg.InviteRefs {
			refs[strings.TrimSpace(ref)] = struct{}{}
		}
		var matches []BindingCandidate
		for _, c := range own {
			if _, ok := refs[c.InvitationID]; ok {
				matches = append(matches, c)
			}
		}
		switch len(matches) {
		case 0:
			return Binding{Outcome: BindUnbound, Reason: ReasonInviteRefUnknown}
		case 1:
			return Binding{Outcome: BindBound, Kind: BindInviteReference, InvitationID: matches[0].InvitationID}
		default:
			return Binding{Outcome: BindAmbiguous, Reason: ReasonSeveralReferences, Candidates: matches}
		}
	}

	var pending []BindingCandidate
	for _, c := range own {
		if c.State == InvitationDelivered {
			pending = append(pending, c)
		}
	}
	switch len(pending) {
	case 1:
		return Binding{Outcome: BindBound, Kind: BindDMSinglePending, InvitationID: pending[0].InvitationID}
	case 0:
		// Answered invitations are listed so the Loop can ask whether this
		// is a correction; it still needs a reply or an explicit reference.
		return Binding{Outcome: BindUnbound, Reason: ReasonNoPendingInvitation, Candidates: own}
	default:
		return Binding{Outcome: BindAmbiguous, Reason: ReasonSeveralPending, Candidates: pending}
	}
}
