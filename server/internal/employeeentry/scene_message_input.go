package employeeentry

import "time"

// SceneMessageInput is one provider message line of a group scene, as the
// wake-time transcript read (M2) parses it. M2 owns this type; this file
// carries an identical copy so the transcript store builds on its own and is
// deleted when both branches merge.
type SceneMessageInput struct {
	ProviderMessageID string
	SentAt            time.Time
	SenderClass       string
	// SenderRef is "dingtalk:<tenant org>:uid|open_id|staff_id:<v>" or "".
	SenderRef       string
	SenderName      string
	QuotedMessageID string
	Body            string
}
