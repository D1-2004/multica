package lark

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// fakeTypingAPIClient records reaction calls and can be programmed to fail.
type fakeTypingAPIClient struct {
	addCalled    []addReactionCall
	deleteCalled []deleteReactionCall
	addErr       error
	deleteErr    error
	addReturn    string
}

type addReactionCall struct {
	creds     InstallationCredentials
	messageID string
	emojiType string
}

type deleteReactionCall struct {
	creds      InstallationCredentials
	messageID  string
	reactionID string
}

func (f *fakeTypingAPIClient) IsConfigured() bool { return true }
func (f *fakeTypingAPIClient) SendInteractiveCard(context.Context, SendCardParams) (string, error) {
	return "", nil
}
func (f *fakeTypingAPIClient) PatchInteractiveCard(context.Context, PatchCardParams) error {
	return nil
}
func (f *fakeTypingAPIClient) SendTextMessage(context.Context, SendTextParams) (string, error) {
	return "", nil
}
func (f *fakeTypingAPIClient) SendMarkdownCard(context.Context, SendMarkdownCardParams) (string, error) {
	return "", nil
}
func (f *fakeTypingAPIClient) SendBindingPromptCard(context.Context, BindingPromptParams) error {
	return nil
}
func (f *fakeTypingAPIClient) GetBotInfo(context.Context, InstallationCredentials) (BotInfo, error) {
	return BotInfo{}, nil
}
func (f *fakeTypingAPIClient) GetMessage(context.Context, InstallationCredentials, string) ([]LarkMessage, error) {
	return nil, nil
}
func (f *fakeTypingAPIClient) ListChatMessages(context.Context, InstallationCredentials, ListMessagesParams) ([]LarkMessage, error) {
	return nil, nil
}
func (f *fakeTypingAPIClient) BatchGetUsers(context.Context, InstallationCredentials, []string) (map[string]string, error) {
	return nil, nil
}
func (f *fakeTypingAPIClient) AddMessageReaction(_ context.Context, p AddReactionParams) (string, error) {
	f.addCalled = append(f.addCalled, addReactionCall{p.InstallationID, p.MessageID, p.EmojiType})
	return f.addReturn, f.addErr
}
func (f *fakeTypingAPIClient) DeleteMessageReaction(_ context.Context, p DeleteReactionParams) error {
	f.deleteCalled = append(f.deleteCalled, deleteReactionCall{p.InstallationID, p.MessageID, p.ReactionID})
	return f.deleteErr
}

type fakeTypingQueries struct {
	binding      ChatSessionBinding
	installation Installation
	bindingErr   error
	installErr   error
}

func (f *fakeTypingQueries) GetLarkChatSessionBindingBySession(context.Context, pgtype.UUID) (ChatSessionBinding, error) {
	return f.binding, f.bindingErr
}
func (f *fakeTypingQueries) GetLarkInstallation(context.Context, pgtype.UUID) (Installation, error) {
	return f.installation, f.installErr
}

type fakeTypingCreds struct{ secret string }

func (f fakeTypingCreds) DecryptAppSecret(inst Installation) (string, error) {
	return f.secret, nil
}

func TestTypingIndicatorAddRecordsState(t *testing.T) {
	api := &fakeTypingAPIClient{addReturn: "reaction-123"}
	queries := &fakeTypingQueries{}
	mgr := NewTypingIndicatorManager(api, fakeTypingCreds{secret: "shh"}, queries, newDiscardLogger())
	store := &fakeTypingStore{}
	mgr.SetStore(store)

	inst := Installation{AppID: "cli_test", Region: "feishu"}
	session := pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, Valid: true}

	mgr.Add(context.Background(), inst, session, "msg-1", "")

	if len(api.addCalled) != 1 {
		t.Fatalf("expected 1 add call, got %d", len(api.addCalled))
	}
	if api.addCalled[0].messageID != "msg-1" || api.addCalled[0].emojiType != typingEmoji {
		t.Fatalf("unexpected add call params: %+v", api.addCalled[0])
	}

	// The pending reaction must be persisted, not held in this process: the
	// replica that clears it is the one that serves the daemon's completion
	// POST, not the lease holder that ingested the message.
	if len(store.rows) != 1 {
		t.Fatalf("expected 1 persisted indicator, got %d", len(store.rows))
	}
	var got TypingIndicatorState
	if err := json.Unmarshal(store.rows[0].Target, &got); err != nil {
		t.Fatalf("decode persisted target: %v", err)
	}
	if got.MessageID != "msg-1" || got.ReactionID != "reaction-123" {
		t.Fatalf("unexpected persisted target: %+v", got)
	}
}

func TestTypingIndicatorAddSkipsOnEmptyMessageID(t *testing.T) {
	api := &fakeTypingAPIClient{addReturn: "reaction-123"}
	mgr := NewTypingIndicatorManager(api, fakeTypingCreds{secret: "shh"}, &fakeTypingQueries{}, newDiscardLogger())

	inst := Installation{AppID: "cli_test", Region: "feishu"}
	session := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}

	mgr.Add(context.Background(), inst, session, "", "")

	if len(api.addCalled) != 0 {
		t.Fatalf("expected 0 add calls, got %d", len(api.addCalled))
	}
}

func TestTypingIndicatorAddSkipsOldMessages(t *testing.T) {
	api := &fakeTypingAPIClient{addReturn: "reaction-123"}
	mgr := NewTypingIndicatorManager(api, fakeTypingCreds{secret: "shh"}, &fakeTypingQueries{}, newDiscardLogger())

	inst := Installation{AppID: "cli_test", Region: "feishu"}
	session := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}

	oldTime := time.Now().Add(-3 * time.Minute).UnixMilli()
	mgr.Add(context.Background(), inst, session, "msg-old", strconv.FormatInt(oldTime, 10))

	if len(api.addCalled) != 0 {
		t.Fatalf("expected 0 add calls for old message, got %d", len(api.addCalled))
	}
}

func TestTypingIndicatorAddLogsOnAPIError(t *testing.T) {
	api := &fakeTypingAPIClient{addErr: errors.New("lark down")}
	mgr := NewTypingIndicatorManager(api, fakeTypingCreds{secret: "shh"}, &fakeTypingQueries{}, newDiscardLogger())
	store := &fakeTypingStore{}
	mgr.SetStore(store)

	inst := Installation{AppID: "cli_test", Region: "feishu"}
	session := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}

	mgr.Add(context.Background(), inst, session, "msg-1", "")

	if len(api.addCalled) != 1 {
		t.Fatalf("expected 1 add call, got %d", len(api.addCalled))
	}
	// The reaction never landed, so nothing may be recorded for a later clear.
	if len(store.rows) != 0 {
		t.Fatalf("expected 0 persisted indicators after an add error, got %d", len(store.rows))
	}
}

func TestTypingIndicatorClearDeletesReactions(t *testing.T) {
	api := &fakeTypingAPIClient{addReturn: "reaction-abc"}
	queries := &fakeTypingQueries{
		binding: ChatSessionBinding{
			InstallationID: pgtype.UUID{Bytes: [16]byte{9, 9, 9, 9}, Valid: true},
		},
		installation: Installation{
			ID:     pgtype.UUID{Bytes: [16]byte{9, 9, 9, 9}, Valid: true},
			AppID:  "cli_test",
			Region: "feishu",
		},
	}
	mgr := NewTypingIndicatorManager(api, fakeTypingCreds{secret: "shh"}, queries, newDiscardLogger())
	store := &fakeTypingStore{}
	mgr.SetStore(store)

	inst := Installation{AppID: "cli_test", Region: "feishu"}
	session := pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4}, Valid: true}

	mgr.Add(context.Background(), inst, session, "msg-1", "")
	if len(api.addCalled) != 1 {
		t.Fatal("add should have been called")
	}

	mgr.Clear(context.Background(), session)

	if len(api.deleteCalled) != 1 {
		t.Fatalf("expected 1 delete call, got %d", len(api.deleteCalled))
	}
	if api.deleteCalled[0].messageID != "msg-1" || api.deleteCalled[0].reactionID != "reaction-abc" {
		t.Fatalf("unexpected delete params: %+v", api.deleteCalled[0])
	}

	// The claim is the delete: a cleared indicator must be gone from the store,
	// so a second replica racing the same clear finds nothing to remove.
	if len(store.rows) != 0 {
		t.Fatalf("expected 0 persisted indicators after clear, got %d", len(store.rows))
	}
}

func TestTypingIndicatorClearNoOpWhenEmpty(t *testing.T) {
	api := &fakeTypingAPIClient{}
	queries := &fakeTypingQueries{
		binding: ChatSessionBinding{
			InstallationID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
		},
		installation: Installation{
			ID:     pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
			AppID:  "cli_test",
			Region: "feishu",
		},
	}
	mgr := NewTypingIndicatorManager(api, fakeTypingCreds{secret: "shh"}, queries, newDiscardLogger())
	store := &fakeTypingStore{}
	mgr.SetStore(store)

	session := pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4}, Valid: true}
	mgr.Clear(context.Background(), session)

	if len(api.deleteCalled) != 0 {
		t.Fatalf("expected 0 delete calls when empty, got %d", len(api.deleteCalled))
	}
}

func TestTypingIndicatorClearLogsOnDeleteError(t *testing.T) {
	api := &fakeTypingAPIClient{addReturn: "reaction-xyz", deleteErr: errors.New("delete failed")}
	queries := &fakeTypingQueries{
		binding: ChatSessionBinding{
			InstallationID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
		},
		installation: Installation{
			ID:     pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
			AppID:  "cli_test",
			Region: "feishu",
		},
	}
	mgr := NewTypingIndicatorManager(api, fakeTypingCreds{secret: "shh"}, queries, newDiscardLogger())
	store := &fakeTypingStore{}
	mgr.SetStore(store)

	inst := Installation{AppID: "cli_test", Region: "feishu"}
	session := pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4}, Valid: true}

	mgr.Add(context.Background(), inst, session, "msg-1", "")
	mgr.Clear(context.Background(), session)

	if len(api.deleteCalled) != 1 {
		t.Fatalf("expected 1 delete call attempt, got %d", len(api.deleteCalled))
	}
}

func TestTypingIndicatorMultipleMessagesPerSession(t *testing.T) {
	api := &fakeTypingAPIClient{addReturn: "reaction-n"}
	queries := &fakeTypingQueries{
		binding: ChatSessionBinding{
			InstallationID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
		},
		installation: Installation{
			ID:     pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
			AppID:  "cli_test",
			Region: "feishu",
		},
	}
	mgr := NewTypingIndicatorManager(api, fakeTypingCreds{secret: "shh"}, queries, newDiscardLogger())
	store := &fakeTypingStore{}
	mgr.SetStore(store)

	inst := Installation{AppID: "cli_test", Region: "feishu"}
	session := pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4}, Valid: true}

	mgr.Add(context.Background(), inst, session, "msg-a", "")
	mgr.Add(context.Background(), inst, session, "msg-b", "")

	if len(api.addCalled) != 2 {
		t.Fatalf("expected 2 add calls, got %d", len(api.addCalled))
	}

	mgr.Clear(context.Background(), session)

	if len(api.deleteCalled) != 2 {
		t.Fatalf("expected 2 delete calls, got %d", len(api.deleteCalled))
	}
}

func TestTypingIndicatorConcurrentAddAndClear(t *testing.T) {
	api := &fakeTypingAPIClient{addReturn: "reaction-concurrent"}
	queries := &fakeTypingQueries{
		binding: ChatSessionBinding{
			InstallationID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
		},
		installation: Installation{
			ID:     pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
			AppID:  "cli_test",
			Region: "feishu",
		},
	}
	mgr := NewTypingIndicatorManager(api, fakeTypingCreds{secret: "shh"}, queries, newDiscardLogger())
	store := &fakeTypingStore{}
	mgr.SetStore(store)

	inst := Installation{AppID: "cli_test", Region: "feishu"}
	session := pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4}, Valid: true}

	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			mgr.Add(context.Background(), inst, session, "msg", "")
		}
		close(done)
	}()
	go func() {
		for i := 0; i < 50; i++ {
			mgr.Clear(context.Background(), session)
		}
	}()
	<-done
	time.Sleep(10 * time.Millisecond)
}

// fakeTypingStore fakes channel_typing_indicator with the DELETE...RETURNING
// semantics the real query has: a take claims the rows and empties the set.
type fakeTypingStore struct {
	rows []db.ChannelTypingIndicator
}

func (f *fakeTypingStore) AddChannelTypingIndicator(_ context.Context, arg db.AddChannelTypingIndicatorParams) error {
	f.rows = append(f.rows, db.ChannelTypingIndicator{
		ChatSessionID:  arg.ChatSessionID,
		ChannelType:    arg.ChannelType,
		InstallationID: arg.InstallationID,
		Target:         arg.Target,
	})
	return nil
}

func (f *fakeTypingStore) TakeChannelTypingIndicators(_ context.Context, arg db.TakeChannelTypingIndicatorsParams) ([]db.ChannelTypingIndicator, error) {
	var taken, kept []db.ChannelTypingIndicator
	for _, row := range f.rows {
		if row.ChatSessionID == arg.ChatSessionID && row.ChannelType == arg.ChannelType {
			taken = append(taken, row)
			continue
		}
		kept = append(kept, row)
	}
	f.rows = kept
	return taken, nil
}
