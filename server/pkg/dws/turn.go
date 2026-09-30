package dws

import (
	"context"
	"strings"
	"sync"
)

// Origin is the inbound message a turn answers.
type Origin struct {
	ConversationID       string `json:"conversationId,omitempty"`
	MessageID            string `json:"messageId,omitempty"`
	SenderOpenDingTalkID string `json:"senderOpenDingTalkId,omitempty"`
}

// Override applies explicit ids on top of o. The origin's message and sender
// are kept only while the conversation stays the origin's, and the sender
// only for the origin's own message.
func (o Origin) Override(conversationID, messageID, senderOpenDingTalkID string) Origin {
	out := Origin{ConversationID: conversationID, MessageID: messageID, SenderOpenDingTalkID: senderOpenDingTalkID}
	if out.ConversationID == "" {
		out.ConversationID = o.ConversationID
	}
	if out.MessageID == "" && out.ConversationID == o.ConversationID {
		out.MessageID = o.MessageID
	}
	if out.SenderOpenDingTalkID == "" && out.MessageID != "" && out.MessageID == o.MessageID {
		out.SenderOpenDingTalkID = o.SenderOpenDingTalkID
	}
	return out
}

// Turn is one reply to one inbound message: Ack it, show Progress on a
// streaming card, deliver the Final answer. It remembers the open card and
// the reactions to clear, so a backend can hold it for the length of a task;
// a stateless caller saves State after each step and resumes with
// ResumeTurn. A Turn is safe for concurrent use.
type Turn struct {
	Origin Origin

	c         *Client
	mu        sync.Mutex
	handle    string
	reactions []string
}

// TurnState is what a Turn remembers between steps.
type TurnState struct {
	// Handle is the open progress card, if any.
	Handle string `json:"handle,omitempty"`
	// Reactions were added by Ack and are removed by Final.
	Reactions []string `json:"reactions,omitempty"`
}

// Turn starts a reply to origin.
func (c *Client) Turn(origin Origin) *Turn { return &Turn{Origin: origin, c: c} }

// ResumeTurn continues a reply whose state a stateless caller saved.
func (c *Client) ResumeTurn(origin Origin, st TurnState) *Turn {
	return &Turn{Origin: origin, c: c, handle: st.Handle, reactions: append([]string(nil), st.Reactions...)}
}

// State is a copy of what the Turn remembers.
func (t *Turn) State() TurnState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return TurnState{Handle: t.handle, Reactions: append([]string(nil), t.reactions...)}
}

// Ack sends the read receipt and, when emoji is set, a "working on it"
// reaction that Final clears.
func (t *Turn) Ack(ctx context.Context, emoji string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	o := t.Origin
	if err := t.c.Messages.MarkRead(ctx, o.ConversationID, o.MessageID); err != nil {
		return err
	}
	if strings.TrimSpace(emoji) == "" {
		return nil
	}
	if err := t.c.Messages.React(ctx, o.ConversationID, o.MessageID, emoji); err != nil {
		return err
	}
	for _, r := range t.reactions {
		if r == emoji {
			return nil
		}
	}
	t.reactions = append(t.reactions, emoji)
	return nil
}

// Progress shows work in progress on a streaming card: the first call posts
// the card, later calls replace its text. text is the whole reply so far.
// A card created before a failed update stays in the State, so the next
// call updates it instead of posting a second one.
func (t *Turn) Progress(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "" {
		return invalid("progress needs text")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.handle == "" {
		card, err := t.c.Cards.Create(ctx, Target{ConversationID: t.Origin.ConversationID})
		if err != nil {
			return err
		}
		t.handle = card.BizID
	}
	return t.c.Cards.Update(ctx, t.handle, text, FlowInputting)
}

// FinalMessage is the answer that ends a turn.
type FinalMessage struct {
	Text  string `json:"text"`
	Title string `json:"title,omitempty"`
	// Failed closes the progress card in the error state.
	Failed bool   `json:"failed,omitempty"`
	UUID   string `json:"uuid,omitempty"`
}

// FinalResult says how the answer was delivered.
type FinalResult struct {
	// Via is "card", "reply" or "send".
	Via    string `json:"via"`
	Handle string `json:"handle,omitempty"`
	Sent   *Sent  `json:"sent,omitempty"`
	// CleanupError reports reactions that could not be removed; the answer
	// was delivered.
	CleanupError string `json:"cleanupError,omitempty"`
}

// Final closes the progress card with the answer, or quote-replies to the
// origin message when there is no card, or sends to the conversation; then
// removes the Ack reactions.
func (t *Turn) Final(ctx context.Context, msg FinalMessage) (FinalResult, error) {
	if strings.TrimSpace(msg.Text) == "" {
		return FinalResult{}, invalid("final needs text")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	o := t.Origin
	var res FinalResult
	switch {
	case t.handle != "":
		status := FlowFinished
		if msg.Failed {
			status = FlowFailed
		}
		if err := t.c.Cards.Update(ctx, t.handle, msg.Text, status); err != nil {
			return FinalResult{}, err
		}
		res = FinalResult{Via: "card", Handle: t.handle}
	case o.MessageID != "":
		sent, err := t.c.Messages.Reply(ctx, ReplyRequest{ConversationID: o.ConversationID, MessageID: o.MessageID,
			SenderOpenDingTalkID: o.SenderOpenDingTalkID, Title: msg.Title, Text: msg.Text, UUID: msg.UUID})
		if err != nil {
			return FinalResult{}, err
		}
		res = FinalResult{Via: "reply", Sent: &sent}
	default:
		sent, err := t.c.Messages.Send(ctx, SendRequest{Target: Target{ConversationID: o.ConversationID}, Title: msg.Title, Text: msg.Text, UUID: msg.UUID})
		if err != nil {
			return FinalResult{}, err
		}
		res = FinalResult{Via: "send", Sent: &sent}
	}
	var failed []string
	if o.MessageID != "" {
		for _, emoji := range t.reactions {
			if err := t.c.Messages.Unreact(ctx, o.ConversationID, o.MessageID, emoji); err != nil {
				failed = append(failed, emoji)
			}
		}
	}
	if len(failed) > 0 {
		res.CleanupError = "could not remove reactions: " + strings.Join(failed, ", ")
	}
	t.handle, t.reactions = "", failed
	return res, nil
}
