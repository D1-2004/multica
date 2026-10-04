package dwsclient

import (
	"strings"
	"testing"
)

func TestVerifiedFileDeliveryRequiresExactProviderFileResource(t *testing.T) {
	good := `{"success":true,"result":{"messages":[{"openMessageId":"sent-file","openConversationId":"cid-original","resources":[{"resourceId":"provider-file","resourceIdType":"fileId","resourceType":"file"}]}]}}`
	for _, tc := range []struct {
		name, body  string
		file, valid bool
	}{
		{"native-file", good, true, true},
		{"text-ack", `{"success":true,"result":{"messages":[{"openMessageId":"sent-file","openConversationId":"cid-original","content":"文件已发送 fileId:fake"}]}}`, false, true},
		{"derived-reference-not-proof", `{"success":true,"result":{"messages":[{"openMessageId":"sent-file","openConversationId":"cid-original","resourceRefs":[{"type":"fileId","resourceId":"fake"}]}]}}`, false, true},
		{"quoted-file", `{"success":true,"result":{"messages":[{"openMessageId":"sent-file","openConversationId":"cid-original","quotedMessage":{"resources":[{"resourceId":"f","resourceIdType":"fileId","resourceType":"file"}]}}]}}`, false, true},
		{"foreign-cid", strings.ReplaceAll(good, "cid-original", "other"), false, false},
		{"foreign-message", strings.ReplaceAll(good, "sent-file", "other"), false, false},
		{"conflicting-alias", strings.ReplaceAll(good, `"openMessageId":`, `"messageId":"another","openMessageId":`), false, false},
		{"no-file-id", strings.ReplaceAll(good, "provider-file", ""), false, true},
		{"image", strings.ReplaceAll(good, `"resourceType":"file"`, `"resourceType":"image"`), false, true},
		{"business-failure", strings.ReplaceAll(good, `"success":true`, `"success":false`), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := parseMessageFile([]byte(tc.body), "cid-original", "sent-file")
			if file != tc.file || (err == nil) != tc.valid {
				t.Fatal(file, err)
			}
		})
	}
}
