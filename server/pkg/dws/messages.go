package dws

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// MessageService sends, reads and manages chat messages.
type MessageService struct{ c *Client }

// Target addresses a conversation: a group or single chat by
// openConversationId, or a single chat by the receiver's openDingTalkId.
// openDingTalkId is relative to the caller: the same person has a different
// id when seen from another identity.
type Target struct {
	ConversationID     string `json:"conversationId,omitempty"`
	UserOpenDingTalkID string `json:"userOpenDingTalkId,omitempty"`
}

func (t Target) empty() bool { return t.ConversationID == "" && t.UserOpenDingTalkID == "" }

func (t Target) apply(args map[string]any) error {
	switch {
	case t.ConversationID != "":
		args["openConversationId"] = t.ConversationID
	case t.UserOpenDingTalkID != "":
		args["receiverOpenDingTalkId"] = t.UserOpenDingTalkID
	default:
		return invalid("target needs conversationId or userOpenDingTalkId")
	}
	return nil
}

// Sent identifies a delivered message.
type Sent struct {
	ConversationID string `json:"conversationId,omitempty"`
	MessageID      string `json:"messageId,omitempty"`
	TaskID         string `json:"taskId,omitempty"`
	UUID           string `json:"uuid,omitempty"`
}

// Message is one chat message.
type Message struct {
	MessageID            string   `json:"messageId"`
	ConversationID       string   `json:"conversationId,omitempty"`
	SenderOpenDingTalkID string   `json:"senderOpenDingTalkId,omitempty"`
	Sender               string   `json:"sender,omitempty"`
	Content              string   `json:"content"`
	CreateTime           string   `json:"createTime,omitempty"`
	SendType             string   `json:"sendType,omitempty"`
	Quoted               *Message `json:"quoted,omitempty"`
}

type wireMessage struct {
	OpenMessageID        string       `json:"openMessageId"`
	OpenConversationID   string       `json:"openConversationId"`
	SenderOpenDingTalkID string       `json:"senderOpenDingTalkId"`
	Sender               string       `json:"sender"`
	Content              string       `json:"content"`
	CreateTime           string       `json:"createTime"`
	SendType             string       `json:"sendType"`
	QuotedMessage        *wireMessage `json:"quotedMessage"`
}

func (w wireMessage) message() Message {
	m := Message{
		MessageID: w.OpenMessageID, ConversationID: w.OpenConversationID,
		SenderOpenDingTalkID: w.SenderOpenDingTalkID, Sender: w.Sender,
		Content: w.Content, CreateTime: w.CreateTime, SendType: w.SendType,
	}
	if w.QuotedMessage != nil {
		q := w.QuotedMessage.message()
		m.Quoted = &q
	}
	return m
}

type wireSent struct {
	OpenMessageID      string `json:"openMessageId"`
	OpenTaskID         string `json:"openTaskId"`
	OpenConversationID string `json:"openConversationId"`
}

// SendRequest is a markdown message. Mentions are rendered as <@id> in the
// text when the caller did not place them.
type SendRequest struct {
	Target
	Title             string   `json:"title,omitempty"`
	Text              string   `json:"text"`
	AtOpenDingTalkIDs []string `json:"at,omitempty"`
	AtAll             bool     `json:"atAll,omitempty"`
	// UUID is the idempotency key (24h). Generated when empty; pass the same
	// value to make a retried send a no-op.
	UUID string `json:"uuid,omitempty"`
}

// Send delivers a markdown message as the Client's identity.
func (s *MessageService) Send(ctx context.Context, req SendRequest) (Sent, error) {
	if strings.TrimSpace(req.Text) == "" {
		return Sent{}, invalid("send needs text")
	}
	args := map[string]any{"msgType": "markdown"}
	if err := req.Target.apply(args); err != nil {
		return Sent{}, err
	}
	// The title previews the text in the conversation list; compute it
	// before mention placeholders are prepended.
	args["content"] = markdownContent(titleOf(req.Title, req.Text), withMentions(req.Text, req.AtOpenDingTalkIDs, req.AtAll))
	if len(req.AtOpenDingTalkIDs) > 0 {
		args["atOpenDingTalkIds"] = req.AtOpenDingTalkIDs
	}
	if req.AtAll {
		args["atAll"] = true
	}
	req.UUID = orUUID(req.UUID)
	args["uuid"] = req.UUID
	return s.send(ctx, args, req.ConversationID, req.UUID)
}

// ReplyRequest quotes an existing message. DingTalk adds the quoted sender's
// @ itself and a quote reply takes no mention list.
type ReplyRequest struct {
	ConversationID string `json:"conversationId,omitempty"`
	MessageID      string `json:"messageId,omitempty"`
	// SenderOpenDingTalkID of the quoted message; looked up when empty.
	SenderOpenDingTalkID string `json:"senderOpenDingTalkId,omitempty"`
	Title                string `json:"title,omitempty"`
	Text                 string `json:"text"`
	UUID                 string `json:"uuid,omitempty"`
}

// Reply sends a markdown quote reply to req.MessageID.
func (s *MessageService) Reply(ctx context.Context, req ReplyRequest) (Sent, error) {
	if req.ConversationID == "" || req.MessageID == "" {
		return Sent{}, invalid("reply needs conversationId and messageId")
	}
	if strings.TrimSpace(req.Text) == "" {
		return Sent{}, invalid("reply needs text")
	}
	if req.SenderOpenDingTalkID == "" {
		msgs, err := s.Get(ctx, req.MessageID)
		if err != nil {
			return Sent{}, fmt.Errorf("dws: resolve quoted sender: %w", err)
		}
		if len(msgs) == 0 || msgs[0].SenderOpenDingTalkID == "" {
			return Sent{}, invalid("quoted message not found")
		}
		req.SenderOpenDingTalkID = msgs[0].SenderOpenDingTalkID
	}
	content, _ := json.Marshal(map[string]string{
		"referenceOpenMessageId":   req.MessageID,
		"srcMsgSendOpenDingTalkId": req.SenderOpenDingTalkID,
		"replyMsgType":             "markdown",
		"title":                    titleOf(req.Title, req.Text),
		"content":                  req.Text,
	})
	req.UUID = orUUID(req.UUID)
	return s.send(ctx, map[string]any{
		"openConversationId": req.ConversationID, "msgType": "reply", "content": string(content), "uuid": req.UUID,
	}, req.ConversationID, req.UUID)
}

func (s *MessageService) send(ctx context.Context, args map[string]any, conversationID, uuid string) (Sent, error) {
	raw, err := s.c.Call(ctx, ServerChat, "send_personal_message", args)
	if err != nil {
		return Sent{}, err
	}
	var w wireSent
	_ = json.Unmarshal(raw, &w)
	sent := Sent{ConversationID: conversationID, MessageID: w.OpenMessageID, TaskID: w.OpenTaskID, UUID: uuid}
	if w.OpenConversationID != "" {
		sent.ConversationID = w.OpenConversationID
	}
	// Older gateways only return a task id; resolve it to the message.
	if sent.MessageID == "" && sent.TaskID != "" {
		raw, err := s.c.Call(ctx, ServerIM, "query_message_send_status", map[string]any{"openTaskId": sent.TaskID})
		if err == nil {
			_ = json.Unmarshal(raw, &w)
			sent.MessageID = w.OpenMessageID
		}
	}
	return sent, nil
}

// Edit replaces the text of a markdown message the identity sent.
func (s *MessageService) Edit(ctx context.Context, conversationID, messageID, title, text string) error {
	if conversationID == "" || messageID == "" || strings.TrimSpace(text) == "" {
		return invalid("edit needs conversationId, messageId and text")
	}
	_, err := s.c.Call(ctx, ServerIM, "edit_message", map[string]any{
		"openConversationId": conversationID, "openMessageId": messageID, "content": markdownContent(titleOf(title, text), text),
	})
	return err
}

// Recall withdraws a message the identity sent.
func (s *MessageService) Recall(ctx context.Context, conversationID, messageID string) error {
	if conversationID == "" || messageID == "" {
		return invalid("recall needs conversationId and messageId")
	}
	_, err := s.c.Call(ctx, ServerIM, "recall_message", map[string]any{"openConversationId": conversationID, "openMessageId": messageID})
	return err
}

// MarkRead marks a message read as the identity (the read receipt).
func (s *MessageService) MarkRead(ctx context.Context, conversationID, messageID string) error {
	if conversationID == "" || messageID == "" {
		return invalid("mark read needs conversationId and messageId")
	}
	_, err := s.c.Call(ctx, ServerIM, "mark_message_read", map[string]any{"openConversationId": conversationID, "openMessageId": messageID})
	return err
}

// React adds an emoji reaction. The gateway accepts any name, so a typo
// becomes a visible reaction: pass names you know.
func (s *MessageService) React(ctx context.Context, conversationID, messageID, emoji string) error {
	return s.reaction(ctx, "add_emoji_reaction", conversationID, messageID, emoji)
}

// Unreact removes an emoji reaction; removing an absent one succeeds.
func (s *MessageService) Unreact(ctx context.Context, conversationID, messageID, emoji string) error {
	return s.reaction(ctx, "remove_emoji_reaction", conversationID, messageID, emoji)
}

func (s *MessageService) reaction(ctx context.Context, tool, conversationID, messageID, emoji string) error {
	if conversationID == "" || messageID == "" || strings.TrimSpace(emoji) == "" {
		return invalid("reaction needs conversationId, messageId and emoji")
	}
	_, err := s.c.Call(ctx, ServerIM, tool, map[string]any{"openConversationId": conversationID, "openMsgId": messageID, "emojiName": emoji})
	return err
}

// Get looks up messages by openMessageId, with the message each one quotes.
func (s *MessageService) Get(ctx context.Context, ids ...string) ([]Message, error) {
	if len(ids) == 0 {
		return nil, invalid("get needs message ids")
	}
	raw, err := s.c.Call(ctx, ServerIM, "list_messages_by_ids", map[string]any{"openMsgIds": ids})
	if err != nil {
		return nil, err
	}
	return decodeMessages(raw)
}

// HistoryQuery selects messages of one conversation.
type HistoryQuery struct {
	ConversationID string
	// Since bounds how far back to read; zero means no bound.
	Since time.Time
	// Limit caps the number of messages (default 50, max 1000). The newest
	// ones are kept.
	Limit int
}

const (
	historyPageSize = 100
	historyMaxPages = 20
	searchMaxPages  = 50
)

// History returns messages of a conversation, oldest first.
func (s *MessageService) History(ctx context.Context, q HistoryQuery) ([]Message, error) {
	if q.ConversationID == "" {
		return nil, invalid("history needs conversationId")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 1000)
	msgs, _, err := s.walk(ctx, q.ConversationID, q.Since, limit, historyMaxPages)
	if err != nil {
		return nil, err
	}
	slices.Reverse(msgs)
	return msgs, nil
}

// walk reads a conversation backwards and returns up to limit messages,
// newest first. complete reports that it reached since (or the start of the
// conversation) rather than a limit.
//
// Following the gateway's nextCursor exactly drops messages at page
// boundaries (measured on the staging gateway: one or two lost per walk at
// every page size), so each page restarts one second after the cursor and
// messages are de-duplicated by id. When a whole page shares one second the
// overlap returns nothing new; then one page is read from the exact
// millisecond cursor instead.
func (s *MessageService) walk(ctx context.Context, conversationID string, since time.Time, limit, maxPages int) ([]Message, bool, error) {
	// A minute ahead absorbs clock skew so a message sent this second is included.
	cursor, exact := s.c.cfg.now().Add(time.Minute), false
	seen := map[string]bool{}
	out := []Message{}
	for page := 0; page < maxPages; page++ {
		if len(out) >= limit {
			return out[:limit], false, nil
		}
		layout := "2006-01-02 15:04:05"
		if exact {
			layout = "2006-01-02 15:04:05.000"
		}
		raw, err := s.c.Call(ctx, ServerChat, "list_conversation_message_v2", map[string]any{
			"openconversation_id": conversationID, "time": cursor.In(shanghai).Format(layout),
			"forward": false, "limit": historyPageSize,
		})
		if err != nil {
			return nil, false, err
		}
		var pg struct {
			Messages   []wireMessage `json:"messages"`
			NextCursor int64         `json:"nextCursor"`
			HasMore    bool          `json:"hasMore"`
		}
		if err := json.Unmarshal(raw, &pg); err != nil {
			return nil, false, fmt.Errorf("dws: decode history: %w", err)
		}
		fresh, reachedSince := 0, false
		for _, w := range pg.Messages {
			if seen[w.OpenMessageID] {
				continue
			}
			m := w.message()
			if !since.IsZero() {
				if at, ok := parseCreateTime(m.CreateTime); ok && at.Before(since) {
					reachedSince = true
					continue
				}
			}
			seen[w.OpenMessageID] = true
			out = append(out, m)
			fresh++
		}
		switch {
		case reachedSince || !pg.HasMore || pg.NextCursor <= 0:
			return out[:min(len(out), limit)], true, nil
		case fresh == 0 && exact:
			// Stuck even at millisecond precision: give up rather than loop.
			return out[:min(len(out), limit)], false, nil
		case fresh == 0:
			cursor, exact = time.UnixMilli(pg.NextCursor), true
		default:
			cursor, exact = time.UnixMilli(pg.NextCursor).Add(time.Second), false
		}
	}
	return out[:min(len(out), limit)], false, nil
}

// SearchQuery finds messages in one conversation by keyword.
type SearchQuery struct {
	ConversationID string
	Keyword        string
	// Since defaults to 7 days ago.
	Since time.Time
	Limit int
}

// SearchResult holds the matches, oldest first.
type SearchResult struct {
	Messages []Message `json:"messages"`
	// Truncated: the scan stopped at its page budget before reaching Since,
	// so older matches may exist.
	Truncated bool `json:"truncated,omitempty"`
}

// Search matches Keyword (case-insensitive) against the conversation's
// history since Since. The gateway's keyword search ignores the conversation
// filter for group conversations, so this scans history instead.
func (s *MessageService) Search(ctx context.Context, q SearchQuery) (SearchResult, error) {
	keyword := strings.ToLower(strings.TrimSpace(q.Keyword))
	if keyword == "" {
		return SearchResult{}, invalid("search needs a keyword")
	}
	if q.ConversationID == "" {
		return SearchResult{}, invalid("history needs conversationId")
	}
	since := q.Since
	if since.IsZero() {
		since = s.c.cfg.now().Add(-7 * 24 * time.Hour)
	}
	all, complete, err := s.walk(ctx, q.ConversationID, since, searchMaxPages*historyPageSize, searchMaxPages)
	if err != nil {
		return SearchResult{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	hits := []Message{}
	for _, m := range all { // newest first: keep the newest matches
		if len(hits) == limit {
			break
		}
		if strings.Contains(strings.ToLower(m.Content), keyword) {
			hits = append(hits, m)
		}
	}
	slices.Reverse(hits)
	return SearchResult{Messages: hits, Truncated: !complete}, nil
}

func decodeMessages(raw json.RawMessage) ([]Message, error) {
	var out struct {
		Messages []wireMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("dws: decode messages: %w", err)
	}
	msgs := make([]Message, 0, len(out.Messages))
	for _, w := range out.Messages {
		msgs = append(msgs, w.message())
	}
	return msgs, nil
}

var (
	shanghai        = time.FixedZone("Asia/Shanghai", 8*60*60)
	markdownMarkers = strings.NewReplacer("**", "", "__", "", "`", "")
)

func parseCreateTime(s string) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, shanghai)
	return t, err == nil
}

func markdownContent(title, text string) string {
	raw, _ := json.Marshal(map[string]string{"title": title, "text": text})
	return string(raw)
}

// titleOf is the conversation-list preview: the caller's title, or the
// first line of text with markdown markers stripped.
func titleOf(title, text string) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	line = strings.TrimSpace(strings.Trim(markdownMarkers.Replace(line), "#>- "))
	r := []rune(line)
	if len(r) > 30 {
		return string(r[:30]) + "…"
	}
	if line == "" {
		return "消息"
	}
	return line
}

func withMentions(text string, ids []string, all bool) string {
	var missing []string
	for _, id := range ids {
		if id != "" && !strings.Contains(text, "<@"+id+">") {
			missing = append(missing, "<@"+id+">")
		}
	}
	if all && !strings.Contains(text, "<@all>") {
		missing = append(missing, "<@all>")
	}
	if len(missing) == 0 {
		return text
	}
	return strings.Join(missing, " ") + " " + text
}

func orUUID(v string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
