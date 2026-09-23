package protocol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Reserve space for the authenticated control and HTTP envelopes.
const DSHNativePromptMaxBytes = 4*1024*1024 - 4096

var ErrDSHNativePrompt = errors.New("invalid DSH native prompt")

// DSHNativePrompt preserves the official SessionPromptRequest wire contract.
// Image decoding and file-receipt ownership remain authoritative in the Host.
// This payload is task input, never an instruction or credential overlay.
type DSHNativePrompt struct {
	RequestID      string                `json:"requestId"`
	SessionID      string                `json:"sessionId"`
	Mode           string                `json:"mode"`
	Content        []DSHNativePromptPart `json:"content"`
	ClientTimeZone *string               `json:"clientTimeZone,omitempty"`
}

type DSHNativePromptPart struct {
	Type      string  `json:"type"`
	Text      *string `json:"text,omitempty"`
	MediaType *string `json:"mediaType,omitempty"`
	Data      *string `json:"data,omitempty"`
	Name      *string `json:"name,omitempty"`
	ReceiptID *string `json:"receiptId,omitempty"`
}

// ValidDSHWorkdir accepts canonical directories within the employee mount.
// The native gateway resolves the Session's actual directory before admission.
func ValidDSHWorkdir(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n\\") && path.Clean(value) == value &&
		(value == "/mnt/multica" || strings.HasPrefix(value, "/mnt/multica/"))
}

func ValidDSHSessionID(value string) bool {
	raw := strings.TrimPrefix(value, "session-")
	id, err := uuid.Parse(raw)
	return err == nil && id != uuid.Nil && raw == id.String()
}

// DSHNativeRequestIdentity preserves native IDs while deriving a stable database UUID.
// Existing browser UUIDs retain their persisted identity unchanged.
func DSHNativeRequestIdentity(sessionID, requestID string) (uuid.UUID, error) {
	if !ValidDSHSessionID(sessionID) || len(requestID) == 0 || len(requestID) > 160 {
		return uuid.Nil, ErrDSHNativePrompt
	}
	for _, c := range requestID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return uuid.Nil, ErrDSHNativePrompt
		}
	}
	if id, err := uuid.Parse(requestID); err == nil {
		if id == uuid.Nil || id.String() != requestID {
			return uuid.Nil, ErrDSHNativePrompt
		}
		return id, nil
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("multica/dsh/request/v1/"+sessionID+"/"+requestID)), nil
}

func DecodeDSHNativePrompt(raw []byte) (*DSHNativePrompt, error) {
	if len(raw) == 0 || len(raw) > DSHNativePromptMaxBytes || !utf8.Valid(raw) {
		return nil, ErrDSHNativePrompt
	}
	// Explicit nulls are not optional fields in the native contract. Reject
	// them before Go's pointer decoding can erase an invalid union member.
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, ErrDSHNativePrompt
	}
	for key, value := range object {
		switch key {
		case "requestId", "sessionId", "mode", "content", "clientTimeZone":
		default:
			return nil, ErrDSHNativePrompt
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, ErrDSHNativePrompt
		}
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(object["content"], &parts) != nil {
		return nil, ErrDSHNativePrompt
	}
	for _, part := range parts {
		if part == nil {
			return nil, ErrDSHNativePrompt
		}
		for key, value := range part {
			switch key {
			case "type", "text", "mediaType", "data", "name", "receiptId":
			default:
				return nil, ErrDSHNativePrompt
			}
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, ErrDSHNativePrompt
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var p DSHNativePrompt
	if d.Decode(&p) != nil || d.Decode(&struct{}{}) != io.EOF || p.Validate() != nil {
		return nil, ErrDSHNativePrompt
	}
	return &p, nil
}

func (p DSHNativePrompt) Validate() error {
	_, err := DSHNativeRequestIdentity(p.SessionID, p.RequestID)
	if err != nil || (p.Mode != "queue" && p.Mode != "steer") || len(p.Content) == 0 {
		return ErrDSHNativePrompt
	}
	if p.ClientTimeZone != nil {
		zone := *p.ClientTimeZone
		if zone != "UTC" && !strings.Contains(zone, "/") {
			return ErrDSHNativePrompt
		}
		if _, err := time.LoadLocation(zone); err != nil {
			return ErrDSHNativePrompt
		}
	}
	present := false
	for _, part := range p.Content {
		switch part.Type {
		case "text":
			if part.Text == nil || part.MediaType != nil || part.Data != nil || part.Name != nil || part.ReceiptID != nil || !utf8.ValidString(*part.Text) || strings.ContainsRune(*part.Text, 0) {
				return ErrDSHNativePrompt
			}
			present = present || strings.TrimSpace(*part.Text) != ""
		case "image":
			if part.Text != nil || part.ReceiptID != nil || part.MediaType == nil || part.Data == nil || *part.Data == "" {
				return ErrDSHNativePrompt
			}
			switch *part.MediaType {
			case "image/png", "image/jpeg", "image/webp", "image/gif":
			default:
				return ErrDSHNativePrompt
			}
			if part.Name != nil && (!utf8.ValidString(*part.Name) || strings.ContainsRune(*part.Name, 0)) {
				return ErrDSHNativePrompt
			}
			if _, err := base64.StdEncoding.Strict().DecodeString(*part.Data); err != nil {
				return ErrDSHNativePrompt
			}
			present = true
		case "file":
			if part.Text != nil || part.MediaType != nil || part.Data != nil || part.Name != nil || part.ReceiptID == nil || strings.TrimSpace(*part.ReceiptID) == "" || !utf8.ValidString(*part.ReceiptID) || strings.ContainsRune(*part.ReceiptID, 0) {
				return ErrDSHNativePrompt
			}
			present = true
		default:
			return ErrDSHNativePrompt
		}
	}
	raw, err := json.Marshal(p)
	if !present || err != nil || len(raw) > DSHNativePromptMaxBytes {
		return ErrDSHNativePrompt
	}
	return nil
}

// Clone validates and detaches mutable slices/pointers at the trust boundary.
func (p DSHNativePrompt) Clone() (*DSHNativePrompt, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, ErrDSHNativePrompt
	}
	return DecodeDSHNativePrompt(raw)
}

// DisplayText is a platform transcript summary; execution uses Content intact.
func (p DSHNativePrompt) DisplayText() string {
	var parts []string
	for _, part := range p.Content {
		switch part.Type {
		case "text":
			if part.Text != nil {
				parts = append(parts, *part.Text)
			}
		case "image":
			parts = append(parts, "[DSH image]")
		case "file":
			parts = append(parts, "[DSH file]")
		}
	}
	return strings.Join(parts, "\n")
}
