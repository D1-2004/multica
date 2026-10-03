package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/dwsclient"
)

const (
	// DWSRangeMaxLimit bounds one LoadRange page.
	DWSRangeMaxLimit = 50
	// DWSRangeContentBytes bounds one decoded message body; callers clip further.
	DWSRangeContentBytes = 4096
	dwsRangeQuotedBytes  = 1024
)

// DingTalkHistoryRangeLoader reads one bounded page of raw provider history for
// Host-side snapshots outside the Coordinator. It reuses the Coordinator's
// identity issuance, shared SDK sessions and cross-org renewal, never its
// memory, cursors or output shape.
type DingTalkHistoryRangeLoader interface {
	LoadRange(context.Context, RangeRequest) (RangePage, error)
}

// RangeRequest reads at most Limit messages strictly older than Before as the
// agent's own DWS identity. The caller has already fenced scene and tenant.
type RangeRequest struct {
	AgentID        pgtype.UUID
	UID            string
	OrgID          string
	ConversationID string
	Before         time.Time
	Limit          int
}

// RangeMessage is one provider message after config-link redaction. Sender
// fields are provider data: they classify a line, they never authorize it.
type RangeMessage struct {
	ID            string
	SentAt        time.Time // zero when the provider time cannot be placed
	SentAtRaw     string
	Sender        string
	SenderUID     string
	SenderID      string
	SenderOpenID  string
	SendType      string // provider sendType/senderType, lower case
	Content       string
	OriginalBytes int
	Truncated     bool
	Quoted        *RangeQuote
}

type RangeQuote struct {
	ID           string
	Sender       string
	SenderUID    string
	SenderID     string
	SenderOpenID string
	Content      string
	Truncated    bool
}

// RangePage lists messages oldest first.
type RangePage struct {
	Messages []RangeMessage
	RawCount int
	HasMore  bool
}

var employeeTranscriptRead = dwsHistoryRead{
	requestPrefix:  "employee-transcript-",
	reason:         "Multica employee scene transcript",
	identitySource: "employee_scene_transcript",
	dirPrefix:      "multica-employee-dws-",
}

// NewDWSHistoryRangeLoader shares the Coordinator loader's transport and
// identity configuration; it is a separate value with its own audit purpose.
func NewDWSHistoryRangeLoader(cfg DWSHistoryConfig) DingTalkHistoryRangeLoader {
	return NewDWSHistoryLoader(cfg).(*dwsHistoryLoader)
}

func (l *dwsHistoryLoader) LoadRange(ctx context.Context, req RangeRequest) (RangePage, error) {
	if l == nil || l.issuer == nil || l.redeemer == nil || l.cli == nil {
		return RangePage{}, errors.New("DWS history loader is not configured")
	}
	conversationID := strings.TrimSpace(req.ConversationID)
	uid := strings.TrimSpace(req.UID)
	orgID := strings.TrimSpace(req.OrgID)
	if conversationID == "" || uid == "" || orgID == "" || !req.AgentID.Valid {
		return RangePage{}, errors.New("DWS history identity or conversation is incomplete")
	}
	if req.Before.IsZero() {
		return RangePage{}, errors.New("DWS history requires a fixed window cutoff")
	}
	if req.Limit <= 0 || req.Limit > DWSRangeMaxLimit {
		return RangePage{}, errors.New("DWS history range limit is out of bounds")
	}
	raw, err := l.list(ctx, employeeTranscriptRead, req.AgentID, uid, orgID, conversationID, req.Before, req.Limit)
	if err != nil {
		return RangePage{}, err
	}
	return parseDWSRange(raw, req.Before)
}

type dwsRangeSender struct {
	Content              string `json:"content"`
	Text                 string `json:"text"`
	OpenMessageID        string `json:"openMessageId"`
	MessageID            string `json:"messageId"`
	Sender               string `json:"sender"`
	SenderUID            string `json:"senderUid"`
	SenderID             string `json:"senderId"`
	SenderOpenDingTalkID string `json:"senderOpenDingTalkId"`
}

type dwsRangeMessage struct {
	dwsRangeSender
	CreateTime    json.RawMessage `json:"createTime"`
	SendType      string          `json:"sendType"`
	SenderType    string          `json:"senderType"`
	QuotedMessage *dwsRangeSender `json:"quotedMessage"`
}

type dwsRangeResponse struct {
	ContractVersion string          `json:"contractVersion"`
	Success         bool            `json:"success"`
	ErrorCode       string          `json:"errorCode"`
	ErrorMsg        string          `json:"errorMsg"`
	HasMore         *bool           `json:"hasMore"`
	Messages        json.RawMessage `json:"messages"`
	Result          json.RawMessage `json:"result"`
}

// parseDWSRange keeps each message's full body up to DWSRangeContentBytes and
// its sender classification fields. Messages at or after before are dropped.
func parseDWSRange(raw []byte, before time.Time) (RangePage, error) {
	var payload dwsRangeResponse
	if json.Unmarshal(raw, &payload) != nil {
		return RangePage{}, errors.New("decode DWS conversation history response")
	}
	messagesRaw := payload.Messages
	hasMore := payload.HasMore
	if len(messagesRaw) == 0 && len(payload.Result) > 0 && payload.Result[0] == '{' {
		var nested struct {
			HasMore  *bool           `json:"hasMore"`
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(payload.Result, &nested); err != nil {
			return RangePage{}, errors.New("decode nested DWS conversation history response")
		}
		messagesRaw = nested.Messages
		if hasMore == nil {
			hasMore = nested.HasMore
		}
	}
	var messages []dwsRangeMessage
	if len(messagesRaw) > 0 && string(messagesRaw) != "null" {
		if err := json.Unmarshal(messagesRaw, &messages); err != nil {
			return RangePage{}, errors.New("decode DWS conversation history messages")
		}
	}
	if !payload.Success && len(messages) == 0 {
		return RangePage{}, dwsclient.HistoryRejected(payload.ErrorCode, payload.ErrorMsg)
	}
	if len(messagesRaw) == 0 || string(messagesRaw) == "null" {
		return RangePage{}, errors.New("DWS conversation history response is missing messages")
	}
	page := RangePage{RawCount: len(messages), HasMore: hasMore != nil && *hasMore}
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		timestamp, timestampRaw := parseHistoryTimestamp(message.CreateTime)
		if timestamp.IsZero() && payload.ContractVersion == "im.message-list.v1" {
			timestamp, _ = time.ParseInLocation("2006-01-02 15:04:05", timestampRaw, shanghai)
		}
		if !timestamp.IsZero() && !timestamp.Before(before) {
			continue
		}
		content, original, truncated := rangeText(message.Content, message.Text, DWSRangeContentBytes)
		if content == "" {
			continue
		}
		item := RangeMessage{
			ID:        firstNonEmpty(message.OpenMessageID, message.MessageID),
			SentAt:    timestamp.UTC(),
			SentAtRaw: timestampRaw,
			Sender:    clipBytes(strings.Join(strings.Fields(message.Sender), " "), 128),
			SenderUID: strings.TrimSpace(message.SenderUID), SenderID: strings.TrimSpace(message.SenderID),
			SenderOpenID:  strings.TrimSpace(message.SenderOpenDingTalkID),
			SendType:      strings.ToLower(firstNonEmpty(message.SendType, message.SenderType)),
			Content:       content,
			OriginalBytes: original,
			Truncated:     truncated,
		}
		if timestamp.IsZero() {
			item.SentAt = time.Time{}
		}
		if quoted := message.QuotedMessage; quoted != nil {
			quotedContent, _, quotedTruncated := rangeText(quoted.Content, quoted.Text, dwsRangeQuotedBytes)
			item.Quoted = &RangeQuote{
				ID:        firstNonEmpty(quoted.OpenMessageID, quoted.MessageID),
				Sender:    clipBytes(strings.Join(strings.Fields(quoted.Sender), " "), 128),
				SenderUID: strings.TrimSpace(quoted.SenderUID), SenderID: strings.TrimSpace(quoted.SenderID),
				SenderOpenID: strings.TrimSpace(quoted.SenderOpenDingTalkID),
				Content:      quotedContent, Truncated: quotedTruncated,
			}
		}
		page.Messages = append(page.Messages, item)
	}
	return page, nil
}

// rangeText redacts configuration links before clipping, so a clipped body
// never retains a partial bearer link.
func rangeText(content, text string, limit int) (string, int, bool) {
	body := RedactConfigLinks(strings.TrimSpace(firstNonEmpty(content, text)))
	original := len(body)
	clipped := clipBytes(body, limit)
	return clipped, original, len(clipped) < original
}

func clipBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return strings.Clone(text[:end])
}
