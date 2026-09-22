package dingtalkresponse

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/dwsclient"
)

func TestDecisionIdentityDoesNotGateConversationTypeOrOwner(t *testing.T) {
	for _, cid := range []string{"external-group", "ordinary-group", "direct-chat", "internal-group"} {
		t.Run(cid, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "dws")
			script := `#!/bin/sh
case "$*" in
 'profile list --format json') echo '{"success":true,"currentProfile":"sender","profiles":[{"profile":"sender","corpId":"subscription-org"}]}' ;;
 'chat message list-by-ids --msg-ids original --format json') echo '{"success":true,"result":{"messages":[{"openMessageId":"original","openConversationId":"` + cid + `","senderOpenDingTalkId":"actual-author"}]}}' ;;
 *) exit 2 ;;
esac
`
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			session := decisionSession{cli: dwsclient.CLI{Path: path}, dir: dir}
			corp, actor, err := session.Verify(context.Background(), cid, "original")
			if err != nil || corp != "subscription-org" || actor != "actual-author" {
				t.Fatalf("valid source rejected: %s %s %v", corp, actor, err)
			}
			if _, _, err := session.Verify(context.Background(), "different-conversation", "original"); err == nil {
				t.Fatal("different conversation accepted")
			}
		})
	}
}
