package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ---- fakes ----

type fakeInstaller struct {
	inst  ResolvedInstallation
	err   error
	calls int
}

func (f *fakeInstaller) ResolveInstallation(_ context.Context, _ channel.InboundMessage) (ResolvedInstallation, error) {
	f.calls++
	return f.inst, f.err
}

type fakeIdentity struct {
	id    ResolvedIdentity
	err   error
	calls int
}

func (f *fakeIdentity) ResolveSender(_ context.Context, _ ResolvedInstallation, _ channel.InboundMessage) (ResolvedIdentity, error) {
	f.calls++
	return f.id, f.err
}

type fakeTaskContext struct {
	value []byte
	err   error
}

func (f *fakeTaskContext) ResolveTaskContext(_ context.Context, _ ResolvedInstallation, _ channel.InboundMessage) ([]byte, error) {
	return f.value, f.err
}

type fakeDedup struct {
	mu              sync.Mutex
	token           pgtype.UUID
	claimErr        error
	markCalls       int
	relCalls        int
	claimCalls      int
	claimNamespace  pgtype.UUID
	markErr         error
	releaseErr      error
}

func (f *fakeDedup) Claim(_ context.Context, installationID pgtype.UUID, _ string) (pgtype.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls++
	f.claimNamespace = installationID
	if f.claimErr != nil {
		return pgtype.UUID{}, f.claimErr
	}
	return f.token, nil
}
func (f *fakeDedup) Mark(_ context.Context, _ pgtype.UUID, _ string, _ pgtype.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markCalls++
	return f.markErr
}
func (f *fakeDedup) Release(_ context.Context, _ pgtype.UUID, _ string, _ pgtype.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.relCalls++
	return f.releaseErr
}
func (f *fakeDedup) marks() int    { f.mu.Lock(); defer f.mu.Unlock(); return f.markCalls }
func (f *fakeDedup) releases() int { f.mu.Lock(); defer f.mu.Unlock(); return f.relCalls }

type fakeBinder struct {
	ensureID     pgtype.UUID
	ensureErr    error
	appendResult AppendResult
	appendErr    error
	appendCalls  int
	lastEnsure   EnsureSessionParams
	lastAppend   AppendParams
}

func (f *fakeBinder) EnsureSession(_ context.Context, p EnsureSessionParams) (pgtype.UUID, error) {
	f.lastEnsure = p
	return f.ensureID, f.ensureErr
}
func (f *fakeBinder) AppendMessage(_ context.Context, p AppendParams) (AppendResult, error) {
	f.appendCalls++
	f.lastAppend = p
	return f.appendResult, f.appendErr
}

type fakePendingFreshStore struct {
	mu          sync.Mutex
	markedInTx  bool
	err         error
	calls       int
	lastPersist PendingFreshSessionParams
}

func (f *fakePendingFreshStore) PersistPendingFreshSession(_ context.Context, p PendingFreshSessionParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastPersist = p
	return f.markedInTx, f.err
}

func (f *fakePendingFreshStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakePendingFreshStore) last() PendingFreshSessionParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastPersist
}

type fakeAuditor struct {
	mu    sync.Mutex
	drops []DropReason
}

func (f *fakeAuditor) RecordDrop(_ context.Context, _ pgtype.UUID, _ channel.InboundMessage, reason DropReason) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drops = append(f.drops, reason)
	return nil
}
func (f *fakeAuditor) last() (DropReason, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.drops) == 0 {
		return "", false
	}
	return f.drops[len(f.drops)-1], true
}

type fakeReplier struct {
	mu      sync.Mutex
	results []Result
}

func (f *fakeReplier) Reply(_ context.Context, _ ResolvedInstallation, _ channel.InboundMessage, res Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = append(f.results, res)
}
func (f *fakeReplier) calls() []Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Result(nil), f.results...)
}

type fakeTyping struct {
	mu      sync.Mutex
	count   int
	settled int
}

func (f *fakeTyping) OnIngested(_ context.Context, _ ResolvedInstallation, _ channel.InboundMessage, _, _ pgtype.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count++
}
func (f *fakeTyping) OnSettled(_ context.Context, _ pgtype.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settled++
}
func (f *fakeTyping) calls() int        { f.mu.Lock(); defer f.mu.Unlock(); return f.count }
func (f *fakeTyping) settledCalls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.settled }

type fakeIssues struct {
	called bool
	params service.IssueCreateParams
	result service.IssueCreateResult
	err    error
}

func (f *fakeIssues) Create(_ context.Context, p service.IssueCreateParams, _ service.IssueCreateOpts) (service.IssueCreateResult, error) {
	f.called = true
	f.params = p
	return f.result, f.err
}

type fakeTasks struct {
	mu           sync.Mutex
	called       bool
	prepared     bool
	forceFresh   bool
	identity     service.ChatTaskIdentity
	taskContext  []byte
	err          error
	prepareErr   error
	preparedTask service.PreparedChannelChatTask
}

func (f *fakeTasks) EnqueueChatTask(_ context.Context, _ db.ChatSession, identity service.ChatTaskIdentity, forceFresh bool, taskContext []byte) (db.AgentTaskQueue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = true
	f.forceFresh = forceFresh
	f.identity = identity
	f.taskContext = append([]byte(nil), taskContext...)
	return db.AgentTaskQueue{}, f.err
}
func (f *fakeTasks) wasCalled() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.called }
func (f *fakeTasks) freshArg() bool  { f.mu.Lock(); defer f.mu.Unlock(); return f.forceFresh }

func (f *fakeTasks) PrepareChannelChatTask(_ context.Context, _ db.ChatSession, identity service.ChatTaskIdentity, forceFresh bool, taskContext []byte) (service.PreparedChannelChatTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared = true
	f.forceFresh = forceFresh
	f.identity = identity
	f.taskContext = append([]byte(nil), taskContext...)
	return f.preparedTask, f.prepareErr
}

func (f *fakeTasks) wasPrepared() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.prepared }

type fakeReader struct {
	session     db.ChatSession
	ws          db.Workspace
	sessErr     error
	originIssue db.Issue
	originErr   error
	previous    db.ChatMessage
	previousErr error
	// Capacity inputs for the flush's OutcomeAgentBusy check. The zero
	// value (maxConcurrent=0) reads as "no capacity limit".
	maxConcurrent int32
	running       int64
}

func (f *fakeReader) GetChatSession(_ context.Context, _ pgtype.UUID) (db.ChatSession, error) {
	return f.session, f.sessErr
}
func (f *fakeReader) GetWorkspace(_ context.Context, _ pgtype.UUID) (db.Workspace, error) {
	return f.ws, nil
}
func (f *fakeReader) GetAgent(_ context.Context, _ pgtype.UUID) (db.Agent, error) {
	return db.Agent{MaxConcurrentTasks: f.maxConcurrent}, nil
}
func (f *fakeReader) CountRunningTasks(_ context.Context, _ pgtype.UUID) (int64, error) {
	return f.running, nil
}
func (f *fakeReader) GetIssueByOrigin(_ context.Context, _ db.GetIssueByOriginParams) (db.Issue, error) {
	return f.originIssue, f.originErr
}
func (f *fakeReader) GetMostRecentUserChatMessage(_ context.Context, _ pgtype.UUID) (db.ChatMessage, error) {
	return f.previous, f.previousErr
}

type fakeUnbinder struct {
	mu      sync.Mutex
	existed bool
	err     error
	count   int
}

func (f *fakeUnbinder) UnbindSender(_ context.Context, _ ResolvedInstallation, _ channel.InboundMessage) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count++
	return f.existed, f.err
}
func (f *fakeUnbinder) calls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.count }

// ---- harness ----

func activeResolved(t *testing.T) ResolvedInstallation {
	return ResolvedInstallation{
		ID:              uuidFromString(t, "11111111-1111-1111-1111-111111111111"),
		WorkspaceID:     uuidFromString(t, "22222222-2222-2222-2222-222222222222"),
		AgentID:         uuidFromString(t, "33333333-3333-3333-3333-333333333333"),
		InstallerUserID: uuidFromString(t, "99999999-9999-9999-9999-999999999999"),
		Active:          true,
	}
}

func p2pMessage(t *testing.T) channel.InboundMessage {
	return channel.InboundMessage{
		EventID:   "evt-1",
		MessageID: "om-1",
		Type:      channel.MsgTypeText,
		Text:      "hello",
		Source: channel.Source{
			ChannelType: channel.TypeFeishu,
			ChatID:      "oc_chat",
			ChatType:    channel.ChatTypeP2P,
			SenderID:    "ou_user_a",
		},
	}
}

type harness struct {
	router   *Router
	inst     *fakeInstaller
	ident    *fakeIdentity
	taskCtx  *fakeTaskContext
	dedup    *fakeDedup
	binder   *fakeBinder
	pending  *fakePendingFreshStore
	audit    *fakeAuditor
	replier  *fakeReplier
	typing   *fakeTyping
	unbinder *fakeUnbinder
	issues   *fakeIssues
	tasks    *fakeTasks
	reader   *fakeReader
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		inst: &fakeInstaller{inst: activeResolved(t)},
		ident: &fakeIdentity{id: func() ResolvedIdentity {
			userID := uuidFromString(t, "44444444-4444-4444-4444-444444444444")
			return ResolvedIdentity{PrincipalUserID: userID, InitiatorUserID: userID}
		}()},
		taskCtx:  &fakeTaskContext{},
		dedup:    &fakeDedup{token: uuidFromString(t, "55555555-5555-5555-5555-555555555555")},
		binder:   &fakeBinder{ensureID: uuidFromString(t, "66666666-6666-6666-6666-666666666666"), appendResult: AppendResult{DedupMarked: true}},
		pending:  &fakePendingFreshStore{markedInTx: true},
		audit:    &fakeAuditor{},
		replier:  &fakeReplier{},
		typing:   &fakeTyping{},
		unbinder: &fakeUnbinder{existed: true},
		issues:   &fakeIssues{},
		tasks:    &fakeTasks{},
		reader:   &fakeReader{ws: db.Workspace{IssuePrefix: "MUL"}, originErr: pgx.ErrNoRows},
	}
	h.router = NewRouter(h.issues, h.tasks, h.reader, RouterConfig{Logger: discardLogger()})
	h.router.Register(channel.TypeFeishu, ResolverSet{
		Installation: h.inst,
		Identity:     h.ident,
		TaskContext:  h.taskCtx,
		Dedup:        h.dedup,
		Session:      h.binder,
		PendingFresh: h.pending,
		Audit:        h.audit,
		Replier:      h.replier,
		Typing:       h.typing,
		Unbind:       h.unbinder,
		OriginType:   "lark_chat",
	})
	return h
}

func enableDurableRuns(h *harness) {
	h.router.mu.Lock()
	set := h.router.sets[channel.TypeFeishu]
	set.DurableRuns = true
	h.router.sets[channel.TypeFeishu] = set
	h.router.mu.Unlock()
}

func TestRouter_NoResolverSet_ReturnsError(t *testing.T) {
	h := newHarness(t)
	msg := p2pMessage(t)
	msg.Source.ChannelType = channel.Type("slack")
	if err := h.router.Handle(context.Background(), msg); !errors.Is(err, ErrNoResolverSet) {
		t.Fatalf("expected ErrNoResolverSet, got %v", err)
	}
}

func TestRouter_InstallationNotFound_Drops(t *testing.T) {
	h := newHarness(t)
	h.inst.err = ErrInstallationNotFound
	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("drop must not be an error: %v", err)
	}
	if r, _ := h.audit.last(); r != DropReasonInvalidEvent {
		t.Fatalf("expected invalid_event audit, got %q", r)
	}
	if h.dedup.claimCalls != 0 {
		t.Fatalf("must not claim dedup before installation routing")
	}
}

func TestRouter_InstallationOverrideSkipsPlatformResolver(t *testing.T) {
	h := newHarness(t)
	h.inst.err = errors.New("platform installation resolver must not run")
	override := ResolvedInstallation{
		ID:              uuidFromString(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		WorkspaceID:     uuidFromString(t, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
		AgentID:         uuidFromString(t, "cccccccc-cccc-4ccc-8ccc-cccccccccccc"),
		InstallerUserID: uuidFromString(t, "dddddddd-dddd-4ddd-8ddd-dddddddddddd"),
		Active:          true,
	}
	identity := ResolvedIdentity{PrincipalUserID: override.InstallerUserID}

	if _, err := h.router.HandleResultWithOptions(context.Background(), p2pMessage(t), HandleOptions{
		InstallationOverride: &override,
		IdentityOverride:     &identity,
	}); err != nil {
		t.Fatalf("trusted installation override: %v", err)
	}
	if h.inst.calls != 0 {
		t.Fatalf("platform installation resolver calls = %d, want 0", h.inst.calls)
	}
	if h.dedup.claimNamespace != override.ID {
		t.Fatalf("dedup namespace = %+v, want authenticated override %+v", h.dedup.claimNamespace, override.ID)
	}
	if h.binder.lastEnsure.Installation != override {
		t.Fatalf("session installation = %+v, want authenticated override %+v", h.binder.lastEnsure.Installation, override)
	}
}

func TestRouter_InstallationOverrideRejectsIncompleteScope(t *testing.T) {
	h := newHarness(t)
	override := ResolvedInstallation{
		ID:              uuidFromString(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		WorkspaceID:     uuidFromString(t, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
		InstallerUserID: uuidFromString(t, "dddddddd-dddd-4ddd-8ddd-dddddddddddd"),
		Active:          true,
	}
	identity := ResolvedIdentity{PrincipalUserID: override.InstallerUserID}

	_, err := h.router.HandleResultWithOptions(context.Background(), p2pMessage(t), HandleOptions{
		InstallationOverride: &override,
		IdentityOverride:     &identity,
	})
	if err == nil || !strings.Contains(err.Error(), "installation override is incomplete") {
		t.Fatalf("incomplete installation override error = %v", err)
	}
	if h.inst.calls != 0 || h.dedup.claimCalls != 0 {
		t.Fatalf("incomplete override reached resolver/dedup: resolver=%d dedup=%d", h.inst.calls, h.dedup.claimCalls)
	}
}

func TestRouter_InstallationOverrideRequiresIdentityOverride(t *testing.T) {
	h := newHarness(t)
	override := ResolvedInstallation{
		ID:              uuidFromString(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		WorkspaceID:     uuidFromString(t, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
		AgentID:         uuidFromString(t, "cccccccc-cccc-4ccc-8ccc-cccccccccccc"),
		InstallerUserID: uuidFromString(t, "dddddddd-dddd-4ddd-8ddd-dddddddddddd"),
		Active:          true,
	}

	_, err := h.router.HandleResultWithOptions(context.Background(), p2pMessage(t), HandleOptions{
		InstallationOverride: &override,
	})
	if err == nil || !strings.Contains(err.Error(), "installation override requires identity override") {
		t.Fatalf("missing identity override error = %v", err)
	}
	if h.inst.calls != 0 || h.dedup.claimCalls != 0 {
		t.Fatalf("missing identity override reached resolver/dedup: resolver=%d dedup=%d", h.inst.calls, h.dedup.claimCalls)
	}
}

func TestRouter_InstallationOverrideRejectsUntrustedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		principal pgtype.UUID
		wantError string
	}{
		{
			name:      "missing principal",
			principal: pgtype.UUID{},
			wantError: "installation override identity has no principal",
		},
		{
			name:      "different principal",
			principal: uuidFromString(t, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"),
			wantError: "installation override identity does not match installer",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			override := ResolvedInstallation{
				ID:              uuidFromString(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
				WorkspaceID:     uuidFromString(t, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
				AgentID:         uuidFromString(t, "cccccccc-cccc-4ccc-8ccc-cccccccccccc"),
				InstallerUserID: uuidFromString(t, "dddddddd-dddd-4ddd-8ddd-dddddddddddd"),
				Active:          true,
			}
			identity := ResolvedIdentity{PrincipalUserID: tc.principal}

			_, err := h.router.HandleResultWithOptions(context.Background(), p2pMessage(t), HandleOptions{
				InstallationOverride: &override,
				IdentityOverride:     &identity,
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("untrusted identity error = %v, want %q", err, tc.wantError)
			}
			if h.inst.calls != 0 || h.dedup.claimCalls != 0 {
				t.Fatalf("untrusted identity reached resolver/dedup: resolver=%d dedup=%d", h.inst.calls, h.dedup.claimCalls)
			}
		})
	}
}

func TestRouter_RevokedInstallation_Drops(t *testing.T) {
	h := newHarness(t)
	h.inst.inst.Active = false
	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r, _ := h.audit.last(); r != DropReasonRevokedInstallation {
		t.Fatalf("expected revoked_installation, got %q", r)
	}
}

func TestRouter_Duplicate_Drops(t *testing.T) {
	h := newHarness(t)
	h.dedup.claimErr = ErrDuplicate
	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r, _ := h.audit.last(); r != DropReasonDuplicate {
		t.Fatalf("expected duplicate, got %q", r)
	}
}

func TestRouter_GroupNotAddressed_Drops(t *testing.T) {
	h := newHarness(t)
	msg := p2pMessage(t)
	msg.Source.ChatType = channel.ChatTypeGroup
	msg.AddressedToBot = false
	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r, _ := h.audit.last(); r != DropReasonNotAddressedInGroup {
		t.Fatalf("expected not_addressed_in_group, got %q", r)
	}
	if h.dedup.marks() != 1 {
		t.Fatalf("group-filter drop must finalize Mark (1), got %d", h.dedup.marks())
	}
}

func TestRouter_UnboundSender_NeedsBinding(t *testing.T) {
	h := newHarness(t)
	h.ident.err = ErrSenderUnbound
	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r, _ := h.audit.last(); r != DropReasonUnboundUser {
		t.Fatalf("expected unbound_user audit, got %q", r)
	}
	if h.dedup.marks() != 1 {
		t.Fatalf("unbound drop must finalize Mark, got %d", h.dedup.marks())
	}
	if !waitFor(time.Second, func() bool {
		for _, r := range h.replier.calls() {
			if r.Outcome == OutcomeNeedsBinding && r.Sender == "ou_user_a" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("expected a NeedsBinding reply targeting the sender")
	}
}

func TestRouter_NonMember_Drops(t *testing.T) {
	h := newHarness(t)
	h.ident.err = ErrSenderNotMember
	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r, _ := h.audit.last(); r != DropReasonNonWorkspaceMember {
		t.Fatalf("expected non_workspace_member, got %q", r)
	}
}

func TestRouter_EnsureSessionError_Releases(t *testing.T) {
	h := newHarness(t)
	h.binder.ensureErr = errors.New("db down")
	err := h.router.Handle(context.Background(), p2pMessage(t))
	if err == nil {
		t.Fatal("ensure-session infra error must surface to the caller")
	}
	if h.dedup.releases() != 1 {
		t.Fatalf("ensure-session error must Release the claim (1), got %d", h.dedup.releases())
	}
}

func TestRouter_DedupReleaseFailureSurfaces(t *testing.T) {
	h := newHarness(t)
	h.binder.ensureErr = errors.New("db down")
	h.dedup.releaseErr = errors.New("dedup store unavailable")

	err := h.router.Handle(context.Background(), p2pMessage(t))
	if !errors.Is(err, ErrDedupFinalize) {
		t.Fatalf("release failure must surface ErrDedupFinalize, got %v", err)
	}
	if !strings.Contains(err.Error(), "ensure chat session") {
		t.Fatalf("original pipeline error must be preserved, got %v", err)
	}
}

func TestRouter_DedupMarkFailureSurfaces(t *testing.T) {
	h := newHarness(t)
	h.dedup.markErr = errors.New("dedup store unavailable")
	msg := p2pMessage(t)
	msg.Source.ChatType = channel.ChatTypeGroup
	msg.AddressedToBot = false

	err := h.router.Handle(context.Background(), msg)
	if !errors.Is(err, ErrDedupFinalize) {
		t.Fatalf("mark failure must surface ErrDedupFinalize, got %v", err)
	}
}

func TestRouter_TaskContextRejected_DropsWithoutReconnecting(t *testing.T) {
	h := newHarness(t)
	h.taskCtx.err = fmt.Errorf("%w: invalid staff ID", ErrTaskContextRejected)

	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("task-context rejection must not be an infrastructure error: %v", err)
	}
	if reason, _ := h.audit.last(); reason != DropReasonTaskContextRejected {
		t.Fatalf("drop reason = %q, want %q", reason, DropReasonTaskContextRejected)
	}
	if h.dedup.marks() != 1 || h.dedup.releases() != 0 {
		t.Fatalf("rejection must Mark without Release; marks=%d releases=%d", h.dedup.marks(), h.dedup.releases())
	}
	if h.binder.appendCalls != 0 || h.tasks.wasCalled() {
		t.Fatalf("rejected task context must not append or enqueue; appends=%d enqueued=%t", h.binder.appendCalls, h.tasks.wasCalled())
	}
}

func TestRouter_TaskContextInfrastructureError_Releases(t *testing.T) {
	h := newHarness(t)
	h.taskCtx.err = errors.New("HSF unavailable")

	err := h.router.Handle(context.Background(), p2pMessage(t))
	if err == nil || !strings.Contains(err.Error(), "resolve chat task context") {
		t.Fatalf("infrastructure error = %v", err)
	}
	if h.dedup.releases() != 1 || h.dedup.marks() != 0 {
		t.Fatalf("infrastructure error must Release without Mark; marks=%d releases=%d", h.dedup.marks(), h.dedup.releases())
	}
	if h.binder.appendCalls != 0 || h.tasks.wasCalled() {
		t.Fatalf("failed task context must not append or enqueue; appends=%d enqueued=%t", h.binder.appendCalls, h.tasks.wasCalled())
	}
}

func TestRouter_Ingested_InTxMark_FinalizeNone(t *testing.T) {
	h := newHarness(t)
	h.reader.session = db.ChatSession{}
	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// AppendMessage marked in-tx (DedupMarked=true) -> no post-pipeline Mark.
	if h.dedup.marks() != 0 {
		t.Fatalf("in-tx Mark must skip post-pipeline finalize Mark, got %d", h.dedup.marks())
	}
	if h.dedup.releases() != 0 {
		t.Fatalf("a durable ingest must not Release, got %d", h.dedup.releases())
	}
	if !h.tasks.wasCalled() {
		t.Fatalf("ingest must trigger a chat run (inline, no batcher)")
	}
	if !waitFor(time.Second, func() bool { return h.typing.calls() == 1 }) {
		t.Fatalf("ingest must show the typing indicator")
	}
}

func TestRouter_DurableRunIsPreparedAndCommittedByAppend(t *testing.T) {
	h := newHarness(t)
	enableDurableRuns(h)
	taskID := uuidFromString(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	h.tasks.preparedTask = service.PreparedChannelChatTask{
		ID: taskID, AgentID: h.inst.inst.AgentID, RuntimeID: uuidFromString(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
		InitiatorUserID: h.ident.id.InitiatorUserID, OriginatorUserID: h.ident.id.PrincipalUserID, DebounceSeconds: 3,
	}
	h.binder.appendResult = AppendResult{DedupMarked: true, TaskID: taskID}
	h.reader.session = db.ChatSession{ID: h.binder.ensureID, AgentID: h.inst.inst.AgentID}

	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !h.tasks.wasPrepared() {
		t.Fatal("durable run must be prepared before append")
	}
	if h.tasks.wasCalled() {
		t.Fatal("durable run must not use the in-memory enqueue path")
	}
	if h.binder.lastAppend.PreparedTask == nil || h.binder.lastAppend.PreparedTask.ID != taskID {
		t.Fatalf("append prepared task = %+v, want %v", h.binder.lastAppend.PreparedTask, taskID)
	}
	if h.dedup.releases() != 0 || h.dedup.marks() != 0 {
		t.Fatalf("atomic durable append must not finalize outside tx; marks=%d releases=%d", h.dedup.marks(), h.dedup.releases())
	}
}

func TestRouter_DurablePrepareFailureReleasesWithoutAppend(t *testing.T) {
	h := newHarness(t)
	enableDurableRuns(h)
	h.reader.session = db.ChatSession{ID: h.binder.ensureID, AgentID: h.inst.inst.AgentID}
	h.tasks.prepareErr = errors.New("overlay database unavailable")

	err := h.router.Handle(context.Background(), p2pMessage(t))
	if err == nil || !strings.Contains(err.Error(), "prepare durable chat task") {
		t.Fatalf("prepare error = %v", err)
	}
	if h.binder.appendCalls != 0 {
		t.Fatalf("message appended without prepared task: calls=%d", h.binder.appendCalls)
	}
	if h.dedup.releases() != 1 || h.dedup.marks() != 0 {
		t.Fatalf("prepare failure must release the claim; marks=%d releases=%d", h.dedup.marks(), h.dedup.releases())
	}
}

func TestRouter_DurableNoRuntimeCommitsMessageAndRepliesOffline(t *testing.T) {
	h := newHarness(t)
	enableDurableRuns(h)
	h.reader.session = db.ChatSession{ID: h.binder.ensureID, AgentID: h.inst.inst.AgentID}
	h.tasks.prepareErr = service.ErrChatTaskAgentNoRuntime

	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("no-runtime is a product outcome, not an infrastructure retry: %v", err)
	}
	if h.binder.appendCalls != 1 || h.binder.lastAppend.PreparedTask != nil {
		t.Fatalf("offline message append = calls %d prepared %+v, want one append without task", h.binder.appendCalls, h.binder.lastAppend.PreparedTask)
	}
	if h.dedup.releases() != 0 || h.dedup.marks() != 0 {
		t.Fatalf("offline message is finalized in append tx; marks=%d releases=%d", h.dedup.marks(), h.dedup.releases())
	}
	if !waitFor(time.Second, func() bool {
		for _, result := range h.replier.calls() {
			if result.Outcome == OutcomeAgentOffline {
				return true
			}
		}
		return false
	}) {
		t.Fatal("expected agent-offline reply")
	}
	if h.typing.calls() != 0 {
		t.Fatal("offline message must not leave a processing indicator")
	}
}

func TestRouter_DurableForceFreshWithoutRuntimePersistsThroughAppend(t *testing.T) {
	h := newHarness(t)
	enableDurableRuns(h)
	h.reader.session = db.ChatSession{ID: h.binder.ensureID, AgentID: h.inst.inst.AgentID}
	h.tasks.prepareErr = service.ErrChatTaskAgentNoRuntime
	msg := p2pMessage(t)
	msg.ForceFresh = true
	msg.Text = "start over with this prompt"

	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if h.binder.appendCalls != 1 || h.binder.lastAppend.PreparedTask != nil {
		t.Fatalf("append calls/prepared = %d/%+v", h.binder.appendCalls, h.binder.lastAppend.PreparedTask)
	}
	if !h.binder.lastAppend.ForceFreshSession {
		t.Fatal("a fresh prompt without a runnable task must persist the request in the append transaction")
	}
}

func TestRouter_ClaimLost_Drops(t *testing.T) {
	h := newHarness(t)
	h.binder.appendErr = ErrClaimLost
	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("ErrClaimLost must be a duplicate drop, not an error: %v", err)
	}
	if r, _ := h.audit.last(); r != DropReasonDuplicate {
		t.Fatalf("expected duplicate, got %q", r)
	}
	if h.dedup.releases() != 0 || h.dedup.marks() != 0 {
		t.Fatalf("ErrClaimLost must finalizeNone (no Mark/Release); marks=%d rel=%d", h.dedup.marks(), h.dedup.releases())
	}
}

func TestRouter_IssueCommand_Creates(t *testing.T) {
	h := newHarness(t)
	h.taskCtx.value = []byte(`{"agent_identity_context_token":"prepared-context-token","agent_identity_context_token_expires_at":4102444800000,"dispatch_outbound":{"mode":"dws"}}`)
	h.binder.appendResult = AppendResult{DedupMarked: true, IssueCommand: &IssueCommand{Title: "Fix login", Description: "details"}}
	h.issues.result = service.IssueCreateResult{Issue: db.Issue{ID: uuidFromString(t, "77777777-7777-7777-7777-777777777777"), Number: 42, Title: "Fix login"}}
	msg := p2pMessage(t)
	msg.Text = "/issue Fix login\ndetails"
	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !h.issues.called {
		t.Fatal("expected issue create")
	}
	if h.issues.params.OriginType.String != "lark_chat" {
		t.Fatalf("origin_type must come from the resolver set, got %q", h.issues.params.OriginType.String)
	}
	if h.issues.params.AgentIdentityContextToken != "prepared-context-token" {
		t.Fatalf("issue task ContextToken = %q", h.issues.params.AgentIdentityContextToken)
	}
	if !strings.Contains(string(h.issues.params.DispatchContext), `"dispatch_outbound":{"mode":"dws"}`) {
		t.Fatalf("issue dispatch context lost prepared input: %s", h.issues.params.DispatchContext)
	}
	if !strings.Contains(string(h.issues.params.DispatchContext), `"agent_identity_context_token_expires_at":4102444800000`) {
		t.Fatalf("issue dispatch context lost ContextToken expiry: %s", h.issues.params.DispatchContext)
	}
	if h.tasks.wasCalled() {
		t.Fatal("/issue must use the issue task only, not enqueue a second chat task")
	}
	if !waitFor(time.Second, func() bool {
		for _, r := range h.replier.calls() {
			if r.IssueIdentifier == "MUL-42" && r.IssueTitle == "Fix login" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("expected an issue-created reply with the workspace-qualified identifier")
	}
}

func TestTaskIdentityContextTokenRejectsInvalidPreparedToken(t *testing.T) {
	for _, taskContext := range []string{
		`{"agent_identity_context_token":42}`,
		`{"agent_identity_context_token":""}`,
		`{"agent_identity_context_token":null}`,
		`{"agent_identity_context_token":"missing-expiry"}`,
		`{"agent_identity_context_token_expires_at":4102444800000}`,
	} {
		if _, err := taskIdentityContextToken([]byte(taskContext)); err == nil {
			t.Fatalf("invalid prepared ContextToken was treated as absent: %s", taskContext)
		}
	}
}

func TestRouter_DurableIssueCommandSkipsDeferredChatTask(t *testing.T) {
	h := newHarness(t)
	enableDurableRuns(h)
	h.reader.session = db.ChatSession{ID: h.binder.ensureID, AgentID: h.inst.inst.AgentID}
	h.binder.appendResult = AppendResult{DedupMarked: true, IssueCommand: &IssueCommand{Title: "Fix durable command"}}
	h.issues.result = service.IssueCreateResult{Issue: db.Issue{
		ID: uuidFromString(t, "77777777-7777-7777-7777-777777777777"), Number: 43, Title: "Fix durable command",
	}}
	msg := p2pMessage(t)
	msg.Text = "/issue Fix durable command"

	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.tasks.wasPrepared() || h.tasks.wasCalled() {
		t.Fatal("durable /issue must not prepare or enqueue a chat task")
	}
	if h.binder.lastAppend.PreparedTask != nil {
		t.Fatalf("durable /issue append carried a chat task: %+v", h.binder.lastAppend.PreparedTask)
	}
	if h.issues.params.OriginID == h.binder.ensureID {
		t.Fatal("durable /issue must use a per-message origin, not the chat session id")
	}
	wantOrigin := durableIssueCommandOriginID(h.inst.inst.ID, msg.MessageID)
	if h.issues.params.OriginID != wantOrigin {
		t.Fatalf("durable /issue origin = %+v, want %+v", h.issues.params.OriginID, wantOrigin)
	}
}

func TestRouter_DurableIssueCreateFailureDoesNotAppendOrMark(t *testing.T) {
	h := newHarness(t)
	enableDurableRuns(h)
	h.issues.err = errors.New("issue database unavailable")
	msg := p2pMessage(t)
	msg.Text = "/issue retry me"

	err := h.router.Handle(context.Background(), msg)
	if err == nil || !strings.Contains(err.Error(), "create durable issue command") {
		t.Fatalf("Handle error = %v, want durable issue create failure", err)
	}
	if h.binder.appendCalls != 0 {
		t.Fatalf("message appended before durable issue creation: calls=%d", h.binder.appendCalls)
	}
	if h.dedup.releases() != 1 || h.dedup.marks() != 0 {
		t.Fatalf("failed pre-append create must release claim: marks=%d releases=%d", h.dedup.marks(), h.dedup.releases())
	}
}

func TestRouter_DurableIssueRetryRecoversCommittedIssueBeforeAppend(t *testing.T) {
	h := newHarness(t)
	enableDurableRuns(h)
	h.reader.originErr = nil
	h.reader.originIssue = db.Issue{
		ID:       uuidFromString(t, "77777777-7777-7777-7777-777777777777"),
		Number:   44,
		Title:    "already committed",
		OriginID: durableIssueCommandOriginID(h.inst.inst.ID, "om-1"),
	}
	h.binder.appendResult = AppendResult{DedupMarked: true, IssueCommand: &IssueCommand{Title: "already committed"}}
	msg := p2pMessage(t)
	msg.Text = "/issue already committed"

	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatalf("retry recovery failed: %v", err)
	}
	if h.issues.called {
		t.Fatal("retry created a second issue instead of recovering by origin")
	}
	if h.binder.appendCalls != 1 {
		t.Fatalf("recovered issue message append calls = %d, want 1", h.binder.appendCalls)
	}
	if h.dedup.releases() != 0 {
		t.Fatalf("successful recovery released processed claim: %d", h.dedup.releases())
	}
}

func TestRouter_DurableBareIssueResolvesPreviousMessageBeforeCreate(t *testing.T) {
	h := newHarness(t)
	enableDurableRuns(h)
	h.reader.previous = db.ChatMessage{Content: "previous request\nmore detail"}
	h.binder.appendResult = AppendResult{DedupMarked: true, IssueCommand: &IssueCommand{Title: "previous request"}}
	h.issues.result = service.IssueCreateResult{Issue: db.Issue{
		ID: uuidFromString(t, "77777777-7777-7777-7777-777777777777"), Number: 45, Title: "previous request",
	}}
	msg := p2pMessage(t)
	msg.Text = "/issue"

	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatalf("bare durable /issue failed: %v", err)
	}
	if h.issues.params.Title != "previous request" {
		t.Fatalf("resolved durable issue title = %q", h.issues.params.Title)
	}
}

func TestRouter_GroupSessionCreatorIsInstaller(t *testing.T) {
	h := newHarness(t)
	msg := p2pMessage(t)
	msg.Source.ChatType = channel.ChatTypeGroup
	msg.AddressedToBot = true
	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.binder.lastEnsure.Sender != h.inst.inst.InstallerUserID {
		t.Fatalf("group session creator must be the installer")
	}
	// And the run initiator is the sender, not the installer.
	if h.tasks.identity.InitiatorUserID != h.ident.id.InitiatorUserID {
		t.Fatalf("run initiator must be the message sender")
	}
}

func TestRouter_SenderIsolatedGroupSessionCreatorIsSender(t *testing.T) {
	h := newHarness(t)
	h.router.mu.Lock()
	set := h.router.sets[channel.TypeFeishu]
	set.GroupSessionsPerSender = true
	h.router.sets[channel.TypeFeishu] = set
	h.router.mu.Unlock()

	msg := p2pMessage(t)
	msg.Source.ChatType = channel.ChatTypeGroup
	msg.AddressedToBot = true
	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.binder.lastEnsure.Sender != h.ident.id.PrincipalUserID {
		t.Fatalf("sender-isolated group session creator must be the sender")
	}
}

func TestRouter_P2PSessionCreatorIsSender(t *testing.T) {
	h := newHarness(t)
	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.binder.lastEnsure.Sender != h.ident.id.PrincipalUserID {
		t.Fatalf("p2p session creator must be the sender")
	}
}

func TestRouter_TrustedIdentityBypassesSenderBinding(t *testing.T) {
	h := newHarness(t)
	h.ident.err = errors.New("sender binding must not run")
	trustedUserID := uuidFromString(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

	_, err := h.router.HandleResultWithOptions(context.Background(), p2pMessage(t), HandleOptions{
		IdentityOverride: &ResolvedIdentity{PrincipalUserID: trustedUserID},
	})
	if err != nil {
		t.Fatalf("trusted dispatch failed: %v", err)
	}
	if h.ident.calls != 0 {
		t.Fatalf("trusted dispatch resolved the channel sender %d times", h.ident.calls)
	}
	if h.binder.lastEnsure.Sender != trustedUserID {
		t.Fatalf("session principal = %v, want %v", h.binder.lastEnsure.Sender, trustedUserID)
	}
	if h.binder.lastAppend.Sender != trustedUserID {
		t.Fatalf("message principal = %v, want %v", h.binder.lastAppend.Sender, trustedUserID)
	}
	if h.tasks.identity.PrincipalUserID != trustedUserID || h.tasks.identity.InitiatorUserID.Valid {
		t.Fatalf("task identity = %+v, want trusted principal without sender initiator", h.tasks.identity)
	}
}

func TestRouter_DWSOutboundSuppressesServerOutbound(t *testing.T) {
	h := newHarness(t)
	trustedUserID := uuidFromString(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

	_, err := h.router.HandleResultWithOptions(context.Background(), p2pMessage(t), HandleOptions{
		IdentityOverride:       &ResolvedIdentity{PrincipalUserID: trustedUserID},
		SuppressServerOutbound: true,
	})
	if err != nil {
		t.Fatalf("DWS-owned dispatch failed: %v", err)
	}
	h.router.Drain()

	if !h.tasks.wasCalled() {
		t.Fatal("DWS-owned outbound suppressed chat task creation")
	}
	if h.typing.calls() != 0 || h.typing.settledCalls() != 0 {
		t.Fatalf("DWS-owned outbound invoked server typing: ingested=%d settled=%d", h.typing.calls(), h.typing.settledCalls())
	}
	if calls := h.replier.calls(); len(calls) != 0 {
		t.Fatalf("DWS-owned outbound invoked robot replier: %+v", calls)
	}
}

func TestRouter_TrustedDispatchTreatsSlashCommandsAsContent(t *testing.T) {
	t.Run("unbind", func(t *testing.T) {
		h := newHarness(t)
		trustedUserID := uuidFromString(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
		msg := p2pMessage(t)
		msg.Text = "/unbind"

		_, err := h.router.HandleResultWithOptions(context.Background(), msg, HandleOptions{
			IdentityOverride:       &ResolvedIdentity{PrincipalUserID: trustedUserID},
			DisableControlCommands: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if h.unbinder.calls() != 0 || !h.tasks.wasCalled() {
			t.Fatalf("trusted /unbind was treated as a channel command: unbind=%d task=%t", h.unbinder.calls(), h.tasks.wasCalled())
		}
	})

	t.Run("issue", func(t *testing.T) {
		h := newHarness(t)
		enableDurableRuns(h)
		h.reader.session = db.ChatSession{ID: h.binder.ensureID, AgentID: h.inst.inst.AgentID}
		trustedUserID := uuidFromString(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
		msg := p2pMessage(t)
		msg.Text = "/issue 这只是提示词"

		_, err := h.router.HandleResultWithOptions(context.Background(), msg, HandleOptions{
			IdentityOverride:       &ResolvedIdentity{PrincipalUserID: trustedUserID},
			DisableControlCommands: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if h.issues.called || !h.tasks.wasPrepared() {
			t.Fatalf("trusted /issue changed the selected chat surface: issue=%t prepared=%t", h.issues.called, h.tasks.wasPrepared())
		}
		if !h.binder.lastAppend.DisableIssueCommand {
			t.Fatal("chat binder was not told to preserve /issue as message content")
		}
	})
}

func TestRouter_UnboundIdentityKeepsInstallerOnlyAsPrincipal(t *testing.T) {
	h := newHarness(t)
	h.ident.id = ResolvedIdentity{PrincipalUserID: h.inst.inst.InstallerUserID}

	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.binder.lastEnsure.Sender != h.inst.inst.InstallerUserID {
		t.Fatalf("session principal = %v, want installer %v", h.binder.lastEnsure.Sender, h.inst.inst.InstallerUserID)
	}
	if h.binder.lastAppend.Sender != h.inst.inst.InstallerUserID {
		t.Fatalf("message principal = %v, want installer %v", h.binder.lastAppend.Sender, h.inst.inst.InstallerUserID)
	}
	if h.tasks.identity.PrincipalUserID != h.inst.inst.InstallerUserID {
		t.Fatalf("task principal = %v, want installer %v", h.tasks.identity.PrincipalUserID, h.inst.inst.InstallerUserID)
	}
	if h.tasks.identity.InitiatorUserID.Valid {
		t.Fatalf("unbound task initiator = %v, want invalid", h.tasks.identity.InitiatorUserID)
	}
}

func TestRouter_FlushOffline_RepliesAgentOffline(t *testing.T) {
	h := newHarness(t)
	h.tasks.err = service.ErrChatTaskAgentNoRuntime
	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Inline flush (no batcher) emits the offline notice synchronously via replier.
	found := false
	for _, r := range h.replier.calls() {
		if r.Outcome == OutcomeAgentOffline {
			found = true
		}
	}
	if !found {
		t.Fatalf("agent-no-runtime must emit an AgentOffline reply")
	}
	// The reaction was added on ingest but no task will run, so the bus-driven
	// clear never fires — the flush must clear the typing indicator itself.
	if h.typing.settledCalls() != 1 {
		t.Fatalf("offline flush must clear the typing indicator, got %d OnSettled calls", h.typing.settledCalls())
	}
}

func TestRouter_FlushArchived_ClearsTyping(t *testing.T) {
	h := newHarness(t)
	h.tasks.err = service.ErrChatTaskAgentArchived
	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.typing.settledCalls() != 1 {
		t.Fatalf("archived flush must clear the typing indicator, got %d OnSettled calls", h.typing.settledCalls())
	}
}

func TestRouter_FlushSuccess_DoesNotClearTyping(t *testing.T) {
	h := newHarness(t)
	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !h.tasks.wasCalled() {
		t.Fatalf("a healthy session must enqueue a task")
	}
	// A successfully enqueued task is cleared by the platform's bus-driven
	// handler on chat-done / task-failed, NOT by the flush.
	if h.typing.settledCalls() != 0 {
		t.Fatalf("successful flush must not clear the typing indicator, got %d OnSettled calls", h.typing.settledCalls())
	}
}

func TestRouter_ForceFresh_Propagates(t *testing.T) {
	h := newHarness(t)
	msg := p2pMessage(t)
	msg.ForceFresh = true
	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !h.tasks.freshArg() {
		t.Fatalf("ForceFresh must propagate to EnqueueChatTask")
	}
}

func TestRouter_DrainJoinsReplies(t *testing.T) {
	h := newHarness(t)
	h.ident.err = ErrSenderUnbound // triggers a NeedsBinding reply goroutine
	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	done := make(chan struct{})
	go func() { h.router.Drain(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not join the reply goroutine")
	}
	if len(h.replier.calls()) != 1 {
		t.Fatalf("expected exactly one reply after drain, got %d", len(h.replier.calls()))
	}
}

func TestRouter_EmptyMessageID_SkipsDedup(t *testing.T) {
	h := newHarness(t)
	msg := p2pMessage(t)
	msg.MessageID = ""
	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.dedup.claimCalls != 0 {
		t.Fatalf("empty message id must skip the dedup claim, got %d", h.dedup.claimCalls)
	}
	if !h.tasks.wasCalled() {
		t.Fatalf("message must still ingest without a dedup key")
	}
}

// TestRouter_BareFreshCommand_ConsumedWithoutRun covers the bare /new (or
// /reset) directive: no chat_message lands, no run is enqueued (an empty
// prompt would burn a run on nothing), the user gets the confirmation
// outcome, and the NEXT message's run starts a fresh agent session.
func TestRouter_BareFreshCommand_ConsumedWithoutRun(t *testing.T) {
	h := newHarness(t)
	msg := p2pMessage(t)
	msg.Text = ""
	msg.ForceFresh = true

	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	h.router.Drain()

	if h.binder.lastAppend.SessionID.Valid {
		t.Error("bare /new must not append a chat message")
	}
	if h.tasks.wasCalled() {
		t.Error("bare /new must not enqueue a run")
	}
	if h.dedup.marks() != 1 {
		t.Errorf("dedup marks = %d, want 1 (command consumed)", h.dedup.marks())
	}
	results := h.replier.calls()
	if len(results) != 1 || results[0].Outcome != OutcomeFreshSession {
		t.Fatalf("replier results = %+v, want one OutcomeFreshSession", results)
	}

	// The next plain message runs fresh exactly once.
	next := p2pMessage(t)
	next.EventID, next.MessageID = "evt-2", "om-2"
	next.Text = "hello again"
	if err := h.router.Handle(context.Background(), next); err != nil {
		t.Fatalf("Handle next: %v", err)
	}
	h.router.Drain()
	if !h.tasks.wasCalled() {
		t.Fatal("next message must enqueue a run")
	}
	if !h.tasks.freshArg() {
		t.Error("next run must carry force_fresh from the consumed /new")
	}

	// And the mark is consumed: a third message runs without fresh.
	third := p2pMessage(t)
	third.EventID, third.MessageID = "evt-3", "om-3"
	third.Text = "third"
	if err := h.router.Handle(context.Background(), third); err != nil {
		t.Fatalf("Handle third: %v", err)
	}
	h.router.Drain()
	if h.tasks.freshArg() {
		t.Error("pending fresh must be consumed by the previous run")
	}
}

func TestRouter_DurableBareFreshCommandPersistsAndMarksDedupInOneTransaction(t *testing.T) {
	h := newHarness(t)
	enableDurableRuns(h)
	msg := p2pMessage(t)
	msg.Text = ""
	msg.ForceFresh = true

	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if h.pending.callCount() != 1 {
		t.Fatalf("pending fresh persists = %d, want 1", h.pending.callCount())
	}
	got := h.pending.last()
	if got.SessionID != h.binder.ensureID || got.InstallationID != h.inst.inst.ID || got.MessageID != msg.MessageID || got.ClaimToken != h.dedup.token {
		t.Fatalf("persist params = %+v", got)
	}
	if h.binder.appendCalls != 0 || h.tasks.wasPrepared() || h.tasks.wasCalled() {
		t.Fatalf("bare reset appended/prepared/enqueued = %d/%t/%t", h.binder.appendCalls, h.tasks.wasPrepared(), h.tasks.wasCalled())
	}
	if h.dedup.marks() != 0 || h.dedup.releases() != 0 {
		t.Fatalf("dedup must be finalized in the persistence transaction; marks=%d releases=%d", h.dedup.marks(), h.dedup.releases())
	}
}

func TestRouter_DurableBareFreshPersistFailureReleasesClaim(t *testing.T) {
	h := newHarness(t)
	enableDurableRuns(h)
	h.pending.err = errors.New("database unavailable")
	msg := p2pMessage(t)
	msg.Text = ""
	msg.ForceFresh = true

	err := h.router.Handle(context.Background(), msg)
	if err == nil || !strings.Contains(err.Error(), "persist pending fresh session") {
		t.Fatalf("Handle error = %v", err)
	}
	if h.dedup.releases() != 1 || h.dedup.marks() != 0 {
		t.Fatalf("failed persistence must release the claim; marks=%d releases=%d", h.dedup.marks(), h.dedup.releases())
	}
}

func TestRouter_DurableBareFreshClaimLostDropsAsDuplicate(t *testing.T) {
	h := newHarness(t)
	enableDurableRuns(h)
	h.pending.err = ErrClaimLost
	msg := p2pMessage(t)
	msg.Text = ""
	msg.ForceFresh = true

	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatalf("ErrClaimLost must be a duplicate outcome: %v", err)
	}
	if reason, _ := h.audit.last(); reason != DropReasonDuplicate {
		t.Fatalf("drop reason = %q, want duplicate", reason)
	}
	if h.dedup.releases() != 0 || h.dedup.marks() != 0 {
		t.Fatalf("lost claim must not be finalized by this worker; marks=%d releases=%d", h.dedup.marks(), h.dedup.releases())
	}
}

// An inbound message must reach a web client watching the same chat without a
// reload. The engine writes through the service layer, so unlike the web send
// path it inherits no handler broadcast — the Router has to publish one.
func TestRouter_InboundMessage_BroadcastsChatMessage(t *testing.T) {
	h := newHarness(t)
	bus := events.New()
	h.router.SetEventBus(bus)

	var mu sync.Mutex
	var got []events.Event
	bus.SubscribeAll(func(ev events.Event) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	})

	msgID := uuidFromString(t, "77777777-7777-7777-7777-777777777777")
	h.binder.appendResult = AppendResult{
		DedupMarked: true,
		MessageID:   msgID,
		Content:     "看一下上海天气",
		CreatedAt:   pgtype.Timestamptz{Time: time.Unix(1_700_000_000, 0), Valid: true},
	}

	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var chatEvent *events.Event
	for i := range got {
		if got[i].Type == protocol.EventChatMessage {
			chatEvent = &got[i]
			break
		}
	}
	if chatEvent == nil {
		t.Fatalf("no chat:message broadcast for an inbound message; saw %d events", len(got))
	}
	if chatEvent.WorkspaceID == "" {
		t.Error("event carries no workspace; a scoped client would never receive it")
	}
	payload, ok := chatEvent.Payload.(protocol.ChatMessagePayload)
	if !ok {
		t.Fatalf("payload type = %T", chatEvent.Payload)
	}
	if payload.MessageID != util.UUIDToString(msgID) {
		t.Errorf("message id = %q", payload.MessageID)
	}
	if payload.Role != "user" || payload.Content != "看一下上海天气" {
		t.Errorf("unexpected payload: %+v", payload)
	}
}

// A message that never committed must not be broadcast: a bubble no reload can
// reproduce is worse than a missing one.
func TestRouter_InboundMessage_NoBroadcastWithoutCommittedMessage(t *testing.T) {
	h := newHarness(t)
	bus := events.New()
	h.router.SetEventBus(bus)

	var mu sync.Mutex
	var chatEvents int
	bus.SubscribeAll(func(ev events.Event) {
		if ev.Type == protocol.EventChatMessage {
			mu.Lock()
			chatEvents++
			mu.Unlock()
		}
	})

	// The append reports no message id — the shape of every path that did not
	// durably write one.
	h.binder.appendResult = AppendResult{DedupMarked: true}

	if err := h.router.Handle(context.Background(), p2pMessage(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if chatEvents != 0 {
		t.Fatalf("broadcast %d chat:message events with no committed row", chatEvents)
	}
}
