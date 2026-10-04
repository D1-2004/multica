package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// transcriptReader is a SessionReader that records Coordinator transcript
// writes (coordinatorChatWriter); its read methods are never called here.
type transcriptReader struct {
	SessionReader
	messages []string
}

func (r *transcriptReader) CreateChatMessage(_ context.Context, arg db.CreateChatMessageParams) (db.ChatMessage, error) {
	r.messages = append(r.messages, arg.Content)
	return db.ChatMessage{ID: uid(9), Content: arg.Content}, nil
}

func (r *transcriptReader) TouchChatSession(context.Context, pgtype.UUID) error { return nil }

// A capability answer the channel engine's Coordinator ends with a
// configuration link reaches the user through the reply only: the stored
// transcript row and its realtime broadcast keep a placeholder.
func TestPersistCoordinatorAssistantRedactsConfigLinks(t *testing.T) {
	const link = "https://app.multica.example/dingtalk/configure?link=AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_abcdE"
	queries := &transcriptReader{}
	router := &Router{reader: queries}
	bus := events.New()
	var published []string
	bus.SubscribeAll(func(e events.Event) {
		if payload, ok := e.Payload.(protocol.ChatMessagePayload); ok {
			published = append(published, payload.Content)
		}
	})
	router.SetEventBus(bus)

	reply := "我可以整理日报。\n\n你的个人能力配置（15 分钟内有效，限用一次）：" + link
	decision := inboundcoord.Decision{
		Action:              inboundcoord.ActionReply,
		UserText:            reply,
		CoordinationActions: []inboundcoord.CoordinationAction{{Kind: "describe_capabilities", Reply: reply}},
	}
	if err := router.persistCoordinatorAssistant(context.Background(), uid(1), uid(2), uid(3), decision); err != nil {
		t.Fatal(err)
	}
	if len(queries.messages) != 1 {
		t.Fatalf("transcript rows = %d", len(queries.messages))
	}
	stored := queries.messages[0]
	if strings.Contains(stored, "configure?link=") || !strings.HasSuffix(stored, inboundcoord.ConfigLinkPlaceholder) {
		t.Fatalf("stored transcript = %q", stored)
	}
	if len(published) != 1 || published[0] != stored {
		t.Fatalf("broadcast = %q, want the stored text", published)
	}
	if decision.UserText != reply {
		t.Fatal("the caller's decision (the reply to send) must keep its link")
	}
}

// The capability answer's Host line is a Markdown link to the DingTalk deep
// link; the stored transcript keeps the label and a placeholder target.
func TestPersistCoordinatorAssistantRedactsDeepLinkConfigLinks(t *testing.T) {
	const link = "https://app.multica.example/dingtalk/configure?link=AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_abcdE"
	queries := &transcriptReader{}
	router := &Router{reader: queries}
	reply := "我可以整理日报。\n\n[本群能力配置](" + inboundcoord.ConfigLinkDeepLink(link) + ")（30 分钟内有效）"
	decision := inboundcoord.Decision{
		Action:              inboundcoord.ActionReply,
		UserText:            reply,
		CoordinationActions: []inboundcoord.CoordinationAction{{Kind: "describe_capabilities", Reply: reply}},
	}
	if err := router.persistCoordinatorAssistant(context.Background(), uid(1), uid(2), uid(3), decision); err != nil {
		t.Fatal(err)
	}
	if len(queries.messages) != 1 {
		t.Fatalf("transcript rows = %d", len(queries.messages))
	}
	stored := queries.messages[0]
	if strings.Contains(stored, "AbCdEfGhIjKlMnOpQrStUvWxYz") || !strings.Contains(stored, "[本群能力配置]("+inboundcoord.ConfigLinkPlaceholder+")") {
		t.Fatalf("stored transcript = %q", stored)
	}
}
