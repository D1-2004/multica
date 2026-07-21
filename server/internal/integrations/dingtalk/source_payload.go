package dingtalk

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const dingtalkAgentSourceSchemaVersion = 1

// dingtalkAgentSourcePayload is the durable, credential-free view of the
// original Stream callback handed to the agent. Payload retains every decoded
// callback field except credential-bearing keys. RedactedFields makes each
// omission explicit without exposing the removed value.
type dingtalkAgentSourcePayload struct {
	SchemaVersion  int      `json:"schema_version"`
	Platform       string   `json:"platform"`
	Payload        any      `json:"payload"`
	RedactedFields []string `json:"redacted_fields,omitempty"`
}

// sanitizeDingTalkAgentSourcePayload parses the original decrypted Stream
// callback exactly once, recursively removes credential-bearing fields, and
// wraps the remaining structure in a versioned envelope. It deliberately does
// not flatten or allowlist message fields: new DingTalk message types and
// fields remain visible to the agent without another server release.
func sanitizeDingTalkAgentSourcePayload(plain []byte) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.UseNumber()

	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if _, ok := payload.(map[string]any); !ok {
		return nil, errors.New("dingtalk source payload must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("dingtalk source payload contains multiple JSON values")
		}
		return nil, err
	}

	redacted := make([]string, 0)
	scrubDingTalkCredentials(payload, "$", &redacted)
	sort.Strings(redacted)

	encoded, err := json.Marshal(dingtalkAgentSourcePayload{
		SchemaVersion:  dingtalkAgentSourceSchemaVersion,
		Platform:       "dingtalk",
		Payload:        payload,
		RedactedFields: redacted,
	})
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func scrubDingTalkCredentials(value any, path string, redacted *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			childPath := path + "." + key
			if isDingTalkCredentialField(key) {
				delete(typed, key)
				*redacted = append(*redacted, childPath)
				continue
			}
			scrubDingTalkCredentials(child, childPath, redacted)
		}
	case []any:
		for index, child := range typed {
			scrubDingTalkCredentials(child, path+"["+strconv.Itoa(index)+"]", redacted)
		}
	}
}

func isDingTalkCredentialField(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, key)

	switch normalized {
	case "authorization", "cookie", "password", "privatekey", "aeskey", "encryptionkey", "sign":
		return true
	}
	for _, suffix := range []string{
		"token",
		"secret",
		"credential",
		"credentials",
		"signature",
		"webhook",
		"downloadcode",
		"downloadurl",
		"signedurl",
		"presignedurl",
	} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}
