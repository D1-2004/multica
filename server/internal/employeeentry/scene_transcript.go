package employeeentry

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A group transcript is bounded provider history the Host reads at wake time
// so the employee sees group material that never @-mentioned it. It is frozen
// into the v1 RecentConversation snapshot as Host data turns: material, never
// requests, authority or memory.
const (
	TranscriptWindow      = 72 * time.Hour
	TranscriptReadLimit   = 41
	TranscriptLineLimit   = 30
	TranscriptBodyBytes   = 8 << 10
	transcriptHumanRunes  = 600
	transcriptOtherRunes  = 240
	transcriptQuotedRunes = 160
	transcriptSpeakerLen  = 64
	transcriptEvidenceCap = 2000
)

var errTranscriptEvidenceBound = errors.New("transcript evidence exceeds its bound")

type TranscriptSenderClass string

const (
	TranscriptHuman   TranscriptSenderClass = "human"
	TranscriptSelf    TranscriptSenderClass = "self"
	TranscriptBot     TranscriptSenderClass = "bot"
	TranscriptUnknown TranscriptSenderClass = "unknown"
)

// TranscriptSource is one provider message as the Host read it, oldest first.
type TranscriptSource struct {
	ID              string
	SentAt          time.Time
	Sender          string
	SenderUID       string
	SenderID        string
	SenderOpenID    string
	SendType        string
	Content         string
	Truncated       bool
	QuotedID        string
	QuotedSender    string
	QuotedContent   string
	QuotedTruncated bool
}

// TranscriptReader is what the Host knows about the reading employee and the
// workspace's other agents. Provider fields only classify a line.
type TranscriptReader struct {
	UID            string
	OpenIDs        map[string]bool
	DisplayName    string
	OtherAgentUIDs map[string]bool
}

// SceneTranscriptEvidence is the Host's own record bounding one transcript.
type SceneTranscriptEvidence struct {
	ResetAt              time.Time
	HostSendIDs          map[string]bool
	AdmittedMessageIDs   map[string]bool
	WithdrawnEvidenceIDs map[string]bool
}

type TranscriptBounds struct {
	// Org qualifies sender refs: dingtalk:<org>:uid|open_id:<v>.
	Org    string
	Before time.Time
	// Since is max(Before-72h, scene reset).
	Since             time.Time
	WindowMessageIDs  map[string]bool
	HistoryMessageIDs map[string]bool
	Evidence          SceneTranscriptEvidence
}

type TranscriptLine struct {
	MessageID     string
	SentAt        time.Time
	Speaker       string
	SenderRef     string
	Class         TranscriptSenderClass
	Text          string
	QuotedSpeaker string
	Quoted        string
	Truncated     bool
}

type SceneTranscript struct {
	Lines                    []TranscriptLine
	Read                     int
	Omitted                  map[string]int
	WithdrawnEvidenceOmitted bool
	Truncated                bool
	BodyBytes                int
}

// ClassifyTranscriptSender is deterministic and ordered: self, bot, human,
// unknown. Self is the employee's own DWS uid or its open id as seen in this
// window, or a digital-employee line carrying exactly its display name.
func ClassifyTranscriptSender(m TranscriptSource, self TranscriptReader) TranscriptSenderClass {
	uid, id, openID := strings.TrimSpace(m.SenderUID), strings.TrimSpace(m.SenderID), strings.TrimSpace(m.SenderOpenID)
	if self.UID != "" && (uid == self.UID || id == self.UID) {
		return TranscriptSelf
	}
	if (id != "" && self.OpenIDs[id]) || (openID != "" && self.OpenIDs[openID]) {
		return TranscriptSelf
	}
	automated := transcriptAutomatedSender(m.SendType)
	if automated && self.DisplayName != "" && transcriptSameName(m.Sender, self.DisplayName) {
		return TranscriptSelf
	}
	if automated || (uid != "" && self.OtherAgentUIDs[uid]) {
		return TranscriptBot
	}
	if uid != "" || id != "" || openID != "" {
		return TranscriptHuman
	}
	return TranscriptUnknown
}

func transcriptAutomatedSender(sendType string) bool {
	switch strings.ToLower(strings.TrimSpace(sendType)) {
	case "bot", "robot", "digital_employee", "digitalemployee", "ai", "assistant", "app", "system":
		return true
	}
	return false
}

func transcriptSameName(a, b string) bool {
	a, b = strings.Join(strings.Fields(a), " "), strings.Join(strings.Fields(b), " ")
	return a != "" && strings.EqualFold(a, b)
}

// BuildSceneTranscript filters, classifies and bounds provider lines. It keeps
// the newest lines within TranscriptLineLimit and TranscriptBodyBytes and
// returns them oldest first. It never reads or writes memory.
func BuildSceneTranscript(sources []TranscriptSource, self TranscriptReader, b TranscriptBounds) SceneTranscript {
	out := SceneTranscript{Omitted: map[string]int{}, Read: len(sources)}
	mention := ""
	if name := strings.TrimSpace(self.DisplayName); name != "" {
		mention = "@" + name
	}
	newestFirst := make([]TranscriptLine, 0, TranscriptLineLimit)
	for i := len(sources) - 1; i >= 0; i-- {
		m := sources[i]
		id := strings.TrimSpace(m.ID)
		reason := ""
		switch {
		case id == "":
			reason = "no_id"
		case m.SentAt.IsZero():
			// Without a time a line cannot be fenced by reset or the window.
			reason = "unknown_time"
		case !m.SentAt.Before(b.Before):
			reason = "after_cutoff"
		case m.SentAt.Before(b.Since) || (!b.Evidence.ResetAt.IsZero() && !m.SentAt.After(b.Evidence.ResetAt)):
			reason = "before_bound"
		case b.WindowMessageIDs[id]:
			reason = "current_window"
		case b.Evidence.WithdrawnEvidenceIDs[id]:
			reason = "withdrawn_memory_evidence"
			out.WithdrawnEvidenceOmitted = true
		case b.HistoryMessageIDs[id] || b.Evidence.AdmittedMessageIDs[id]:
			reason = "admitted"
		case b.Evidence.HostSendIDs[id]:
			reason = "host_send"
		case mention != "" && strings.Contains(m.Content, mention):
			// Lines that @ the employee are requests, not overheard material.
			reason = "addressed"
		}
		if reason != "" {
			out.Omitted[reason]++
			continue
		}
		if len(newestFirst) == TranscriptLineLimit {
			out.Truncated = true
			out.Omitted["line_budget"]++
			continue
		}
		class := ClassifyTranscriptSender(m, self)
		limit := transcriptOtherRunes
		if class == TranscriptHuman {
			limit = transcriptHumanRunes
		}
		text, clipped := clipTranscriptRunes(strings.TrimSpace(m.Content), limit)
		if text == "" {
			out.Omitted["empty"]++
			continue
		}
		line := TranscriptLine{MessageID: id, SentAt: m.SentAt.UTC(), Speaker: transcriptSpeaker(m.Sender), SenderRef: transcriptSenderRef(b.Org, m), Class: class, Text: text, Truncated: clipped || m.Truncated}
		if quoted := strings.TrimSpace(m.QuotedContent); quoted != "" {
			line.Quoted, clipped = clipTranscriptRunes(quoted, transcriptQuotedRunes)
			line.QuotedSpeaker = transcriptSpeaker(m.QuotedSender)
			line.Truncated = line.Truncated || clipped || m.QuotedTruncated
		}
		size := len(line.Text) + len(line.Quoted)
		if out.BodyBytes+size > TranscriptBodyBytes {
			out.Truncated = true
			out.Omitted["byte_budget"]++
			continue
		}
		out.BodyBytes += size
		newestFirst = append(newestFirst, line)
	}
	for i, j := 0, len(newestFirst)-1; i < j; i, j = i+1, j-1 {
		newestFirst[i], newestFirst[j] = newestFirst[j], newestFirst[i]
	}
	out.Lines = newestFirst
	return out
}

// SceneMessageInput is one provider line handed to durable scene history
// storage after the wake snapshot is saved. Body is the full read body.
type SceneMessageInput struct {
	ProviderMessageID string
	SentAt            time.Time
	SenderClass       string
	SenderRef         string
	SenderName        string
	QuotedMessageID   string
	Body              string
}

// SceneMessagesFromPage returns every read line that has an id and a parsed
// time, including lines the transcript omits; storage applies its own rules.
func SceneMessagesFromPage(org string, sources []TranscriptSource, reader TranscriptReader) []SceneMessageInput {
	out := make([]SceneMessageInput, 0, len(sources))
	for _, m := range sources {
		id := strings.TrimSpace(m.ID)
		if id == "" || m.SentAt.IsZero() {
			continue
		}
		out = append(out, SceneMessageInput{ProviderMessageID: id, SentAt: m.SentAt.UTC(), SenderClass: string(ClassifyTranscriptSender(m, reader)),
			SenderRef: transcriptSenderRef(org, m), SenderName: transcriptSpeaker(m.Sender), QuotedMessageID: strings.TrimSpace(m.QuotedID), Body: m.Content})
	}
	return out
}

// transcriptSenderRef follows employeeRequesterRef: a provider uid first, else
// the viewer-relative open id the employee's own DWS identity observed.
func transcriptSenderRef(org string, m TranscriptSource) string {
	org = strings.TrimSpace(org)
	if org == "" {
		return ""
	}
	if uid := strings.TrimSpace(m.SenderUID); uid != "" {
		return "dingtalk:" + org + ":uid:" + uid
	}
	if open := strings.TrimSpace(m.SenderOpenID); open != "" {
		return "dingtalk:" + org + ":open_id:" + open
	}
	return ""
}

// transcriptSpeaker keeps a display name on one line and out of label syntax.
func transcriptSpeaker(name string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '[', ']', '·':
			return ' '
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, name)
	name, _ = clipTranscriptRunes(strings.Join(strings.Fields(name), " "), transcriptSpeakerLen)
	return name
}

func clipTranscriptRunes(text string, limit int) (string, bool) {
	if utf8.RuneCountInString(text) <= limit {
		return text, false
	}
	return string([]rune(text)[:limit]), true
}

// SceneTranscriptEvidence reads the Host's own facts that bound a transcript:
// the scene memory reset, every Host send and admitted message of the scene in
// the window (any principal), and evidence of forgotten or superseded memory.
func (s *Store) SceneTranscriptEvidence(ctx context.Context, scope Scope, since, before time.Time) (SceneTranscriptEvidence, error) {
	out := SceneTranscriptEvidence{HostSendIDs: map[string]bool{}, AdmittedMessageIDs: map[string]bool{}, WithdrawnEvidenceIDs: map[string]bool{}}
	if s == nil || s.db == nil || !validScope(scope) || since.IsZero() || before.IsZero() || !since.Before(before) {
		return out, ErrInvalid
	}
	var err error
	if out.ResetAt, err = s.memoryResetAt(ctx, scope, ""); err != nil {
		return out, err
	}
	read := func(target map[string]bool, query string, args ...any) error {
		rows, err := s.db.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			target[id] = true
			count++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if count >= transcriptEvidenceCap {
			// A partial set could resurface withdrawn or duplicate lines.
			return errTranscriptEvidenceBound
		}
		return nil
	}
	// Provider times have second precision and Host rows commit around the
	// send, so the Host-side windows are widened by an hour on the old side.
	if err = read(out.HostSendIDs, `SELECT a.provider_message_id FROM response_action a JOIN agent_scene sc ON sc.id=$4::uuid AND sc.workspace_id=$1::uuid AND sc.agent_id=$2::uuid AND sc.tenant_org_id=$3
 WHERE a.workspace_id=$1::uuid AND a.agent_id=$2::uuid AND a.kind='message.send' AND a.provider_message_id<>'' AND a.provider_conversation_id=sc.external_scene_id
 AND a.input->>'scene_id'=$4::text AND a.created_at>=$5::timestamptz-interval '1 hour' AND a.created_at<$6 ORDER BY a.created_at DESC LIMIT $7`,
		append(scopeArgs(scope), since, before, transcriptEvidenceCap)...); err != nil {
		return out, err
	}
	if err = read(out.AdmittedMessageIDs, `SELECT DISTINCT m.value->>'openMsgId' FROM employee_event_consumption c
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(c.payload#>'{command,event,data,messages}')='array' THEN c.payload#>'{command,event,data,messages}' ELSE '[]'::jsonb END) m(value)
 WHERE c.workspace_id=$1::uuid AND c.agent_id=$2::uuid AND c.tenant_org_id=$3 AND c.scene_id=$4::uuid AND c.created_at>=$5::timestamptz-interval '1 hour' AND c.created_at<$6::timestamptz+interval '1 minute'
 AND COALESCE(m.value->>'openMsgId','')<>'' LIMIT $7`, append(scopeArgs(scope), since, before, transcriptEvidenceCap)...); err != nil {
		return out, err
	}
	err = read(out.WithdrawnEvidenceIDs, `SELECT DISTINCT record->>'evidence_id' FROM employee_learning
 WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid AND (superseded_by IS NOT NULL OR forgotten_at IS NOT NULL)
 AND COALESCE(record->>'evidence_id','')<>'' LIMIT $5`, append(scopeArgs(scope), transcriptEvidenceCap)...)
	return out, err
}
