package agentmessagerouter

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	dto "github.com/prometheus/client_model/go"

	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const canonicalCallbackToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type fakeBindingStore struct {
	row             db.ChannelInstallation
	beginErr        error
	getErr          error
	activateErr     error
	activateHook    func(*fakeBindingStore)
	revokeErr       error
	updateSurfaceErr error
	listErr         error
	beginConfig     []byte
	revokeArg       db.RevokeDingTalkAccountBindingParams
	activated       bool
	revoked         bool
	updatedSurface  bool
	identityAttempt db.AgentDingtalkIdentityAttempt
	identity        db.AgentDingtalkIdentity
	cleanupErr      error
	cleanupCalls    int
}

func (f *fakeBindingStore) UpdateDingTalkAccountBindingSurface(_ context.Context, arg db.UpdateDingTalkAccountBindingSurfaceParams) (db.ChannelInstallation, error) {
	if f.updateSurfaceErr != nil {
		return db.ChannelInstallation{}, f.updateSurfaceErr
	}
	if !f.row.ID.Valid || f.row.WorkspaceID != arg.WorkspaceID || f.row.AgentID != arg.AgentID ||
		f.row.ChannelType != ChannelTypeDingTalkAccount || f.row.Status != "active" {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	config, err := ParseDingTalkAccountConfig(f.row.Config)
	if err != nil || config.RouterSourceID != arg.ExpectedRouterSourceID {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	config.SurfaceType = arg.SurfaceType
	f.row.Config, err = config.Marshal()
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	f.updatedSurface = true
	return f.row, nil
}

func (f *fakeBindingStore) BeginDingTalkAccountBinding(_ context.Context, arg db.BeginDingTalkAccountBindingParams) (db.ChannelInstallation, error) {
	if f.beginErr != nil {
		return db.ChannelInstallation{}, f.beginErr
	}
	f.beginConfig = append([]byte(nil), arg.Config...)
	if f.row.ID.Valid {
		old, err := ParseDingTalkAccountConfig(f.row.Config)
		if err != nil {
			return db.ChannelInstallation{}, err
		}
		pending, err := ParseDingTalkAccountConfig(arg.Config)
		if err != nil {
			return db.ChannelInstallation{}, err
		}
		pending.DispatchEndpointID = old.DispatchEndpointID
		pending.DispatchKeyID = old.DispatchKeyID
		pending.DispatchURL = old.DispatchURL
		arg.Config, err = pending.Marshal()
		if err != nil {
			return db.ChannelInstallation{}, err
		}
		f.row.Config = arg.Config
		f.row.Status = "pending"
		f.row.InstallerUserID = arg.InstallerUserID
		return f.row, nil
	}
	f.row = bindingRow(
		uuidStringForTest(arg.WorkspaceID),
		uuidStringForTest(arg.AgentID),
		"11111111-1111-1111-1111-111111111111",
		"pending",
		arg.Config,
	)
	f.row.InstallerUserID = arg.InstallerUserID
	return f.row, nil
}

func (f *fakeBindingStore) UpdateDingTalkAccountBindingDispatchURL(_ context.Context, arg db.UpdateDingTalkAccountBindingDispatchURLParams) (db.ChannelInstallation, error) {
	if !f.row.ID.Valid || f.row.ID != arg.ID || f.row.WorkspaceID != arg.WorkspaceID ||
		f.row.AgentID != arg.AgentID || f.row.ChannelType != ChannelTypeDingTalkAccount || f.row.Status != "pending" {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	config, err := ParseDingTalkAccountConfig(f.row.Config)
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	if config.DispatchEndpointID != arg.DispatchEndpointID {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	config.DispatchURL = arg.DispatchUrl
	f.row.Config, err = config.Marshal()
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	return f.row, nil
}

func (f *fakeBindingStore) GetDingTalkAccountBinding(_ context.Context, _ pgtype.UUID) (db.ChannelInstallation, error) {
	if f.getErr != nil {
		return db.ChannelInstallation{}, f.getErr
	}
	if !f.row.ID.Valid {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	return f.row, nil
}

func (f *fakeBindingStore) GetDingTalkAccountBindingByAgent(_ context.Context, _ db.GetDingTalkAccountBindingByAgentParams) (db.ChannelInstallation, error) {
	if f.getErr != nil {
		return db.ChannelInstallation{}, f.getErr
	}
	if !f.row.ID.Valid {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	return f.row, nil
}

func (f *fakeBindingStore) GetDingTalkAccountBindingInWorkspace(_ context.Context, _ db.GetDingTalkAccountBindingInWorkspaceParams) (db.ChannelInstallation, error) {
	if f.getErr != nil {
		return db.ChannelInstallation{}, f.getErr
	}
	if !f.row.ID.Valid {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	return f.row, nil
}

func (f *fakeBindingStore) ListDingTalkAccountBindings(_ context.Context, _ pgtype.UUID) ([]db.ChannelInstallation, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if !f.row.ID.Valid {
		return nil, nil
	}
	return []db.ChannelInstallation{f.row}, nil
}

func (f *fakeBindingStore) ClearExpiredDingTalkAccountCallbackCredentials(_ context.Context, arg db.ClearExpiredDingTalkAccountCallbackCredentialsParams) error {
	f.cleanupCalls++
	if f.cleanupErr != nil {
		return f.cleanupErr
	}
	if !f.row.ID.Valid || !arg.ExpiredBefore.Valid {
		return nil
	}
	config, err := ParseDingTalkAccountConfig(f.row.Config)
	if err != nil {
		return err
	}
	if config.CallbackExpiresAt.IsZero() || config.CallbackExpiresAt.After(arg.ExpiredBefore.Time) {
		return nil
	}
	config.CallbackTokenHash = ""
	config.CallbackExpiresAt = time.Time{}
	f.row.Config, err = config.Marshal()
	return err
}

func (f *fakeBindingStore) ActivateDingTalkAccountBinding(_ context.Context, arg db.ActivateDingTalkAccountBindingParams) (db.ChannelInstallation, error) {
	if f.activateHook != nil {
		f.activateHook(f)
	}
	if f.activateErr != nil {
		return db.ChannelInstallation{}, f.activateErr
	}
	f.row.Config = append([]byte(nil), arg.Config...)
	f.row.Status = "active"
	f.activated = true
	return f.row, nil
}

func (f *fakeBindingStore) CompleteDingTalkAccountBindingResult(_ context.Context, arg db.CompleteDingTalkAccountBindingResultParams) (db.ChannelInstallation, error) {
	f.row.Config = append([]byte(nil), arg.Config...)
	f.row.Status = arg.Status
	return f.row, nil
}

func (f *fakeBindingStore) RevokeDingTalkAccountBinding(_ context.Context, arg db.RevokeDingTalkAccountBindingParams) (db.ChannelInstallation, error) {
	if f.revokeErr != nil {
		return db.ChannelInstallation{}, f.revokeErr
	}
	f.revokeArg = arg
	config, err := ParseDingTalkAccountConfig(f.row.Config)
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	f.row.Config, err = json.Marshal(map[string]any{
		"schema_version":       config.SchemaVersion,
		"dispatch_endpoint_id": config.DispatchEndpointID,
		"dispatch_key_id":      config.DispatchKeyID,
		"dispatch_url":         config.DispatchURL,
	})
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	f.row.Status = "revoked"
	f.revoked = true
	return f.row, nil
}

func (f *fakeBindingStore) BeginAgentDingTalkIdentityAttempt(_ context.Context, arg db.BeginAgentDingTalkIdentityAttemptParams) (db.AgentDingtalkIdentityAttempt, error) {
	f.identityAttempt = db.AgentDingtalkIdentityAttempt{
		ID:                mustUUIDForTest("22222222-2222-2222-2222-222222222222"),
		WorkspaceID:       arg.WorkspaceID,
		AgentID:           arg.AgentID,
		InitiatorUserID:   arg.InitiatorUserID,
		CallbackTokenHash: arg.CallbackTokenHash,
		ExpiresAt:         arg.ExpiresAt,
	}
	return f.identityAttempt, nil
}

func (f *fakeBindingStore) GetAgentDingTalkIdentityAttempt(_ context.Context, _ pgtype.UUID) (db.AgentDingtalkIdentityAttempt, error) {
	if !f.identityAttempt.ID.Valid {
		return db.AgentDingtalkIdentityAttempt{}, pgx.ErrNoRows
	}
	return f.identityAttempt, nil
}

func (f *fakeBindingStore) GetAgentDingTalkIdentityAttemptByAgent(_ context.Context, arg db.GetAgentDingTalkIdentityAttemptByAgentParams) (db.AgentDingtalkIdentityAttempt, error) {
	if !f.identityAttempt.ID.Valid || f.identityAttempt.WorkspaceID != arg.WorkspaceID || f.identityAttempt.AgentID != arg.AgentID {
		return db.AgentDingtalkIdentityAttempt{}, pgx.ErrNoRows
	}
	return f.identityAttempt, nil
}

func (f *fakeBindingStore) CompleteAgentDingTalkIdentityAttempt(_ context.Context, arg db.CompleteAgentDingTalkIdentityAttemptParams) (db.CompleteAgentDingTalkIdentityAttemptRow, error) {
	now := time.Now().UTC()
	f.identityAttempt.CompletedUid = arg.DwsUid
	f.identityAttempt.CompletedOrgID = arg.OrgID
	f.identityAttempt.UsedAt = pgtype.Timestamptz{Time: now, Valid: true}
	f.identity = db.AgentDingtalkIdentity{
		AgentID:            f.identityAttempt.AgentID,
		WorkspaceID:        f.identityAttempt.WorkspaceID,
		DwsUid:             arg.DwsUid.String,
		OrgID:              arg.OrgID.String,
		OrganizationName:   arg.OrganizationName,
		AccountDisplayName: arg.AccountDisplayName,
		AccountAvatarUrl:   arg.AccountAvatarUrl,
		BoundBy:            f.identityAttempt.InitiatorUserID,
		BoundAt:            pgtype.Timestamptz{Time: now, Valid: true},
	}
	return db.CompleteAgentDingTalkIdentityAttemptRow{
		AgentID:            f.identity.AgentID,
		WorkspaceID:        f.identity.WorkspaceID,
		DwsUid:             f.identity.DwsUid,
		OrgID:              f.identity.OrgID,
		OrganizationName:   f.identity.OrganizationName,
		AccountDisplayName: f.identity.AccountDisplayName,
		AccountAvatarUrl:   f.identity.AccountAvatarUrl,
		BoundBy:            f.identity.BoundBy,
		BoundAt:            f.identity.BoundAt,
	}, nil
}

func (f *fakeBindingStore) GetAgentDingTalkIdentity(_ context.Context, _ db.GetAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error) {
	if !f.identity.AgentID.Valid {
		return db.AgentDingtalkIdentity{}, pgx.ErrNoRows
	}
	return f.identity, nil
}

func (f *fakeBindingStore) ListAgentDingTalkIdentities(_ context.Context, _ pgtype.UUID) ([]db.AgentDingtalkIdentity, error) {
	if !f.identity.AgentID.Valid {
		return nil, nil
	}
	return []db.AgentDingtalkIdentity{f.identity}, nil
}

func (f *fakeBindingStore) DeleteAgentDingTalkIdentity(_ context.Context, _ db.DeleteAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error) {
	if !f.identity.AgentID.Valid {
		return db.AgentDingtalkIdentity{}, pgx.ErrNoRows
	}
	deleted := f.identity
	f.identity = db.AgentDingtalkIdentity{}
	f.identityAttempt = db.AgentDingtalkIdentityAttempt{}
	return deleted, nil
}

func (f *fakeBindingStore) DeleteAgentDingTalkIdentityAttempts(_ context.Context, _ db.DeleteAgentDingTalkIdentityAttemptsParams) error {
	f.identityAttempt = db.AgentDingtalkIdentityAttempt{}
	return nil
}

type fakeBindingRouter struct {
	issued       BindingToken
	issueErr     error
	subscription Subscription
	getErr       error
	deleteErr    error
	updateErr    error
	issueAgent   string
	issueURL     string
	getCalls     int
	deleted      []string
	updated      []string
}

func (f *fakeBindingRouter) IssueBindingToken(_ context.Context, agentID, dispatchURL string) (BindingToken, error) {
	f.issueAgent = agentID
	f.issueURL = dispatchURL
	return f.issued, f.issueErr
}

func (f *fakeBindingRouter) GetSubscription(_ context.Context, sourceID string) (Subscription, error) {
	f.getCalls++
	if f.subscription.SourceID == "" {
		f.subscription.SourceID = sourceID
	}
	return f.subscription, f.getErr
}

func (f *fakeBindingRouter) UpdateSubscriptionSurface(_ context.Context, sourceID, agentID, surfaceType string) (Subscription, error) {
	f.updated = append(f.updated, sourceID+":"+agentID+":"+surfaceType)
	if f.updateErr != nil {
		return Subscription{}, f.updateErr
	}
	f.subscription.SourceID = sourceID
	f.subscription.AgentID = agentID
	f.subscription.Surface.Type = surfaceType
	return f.subscription, nil
}

func TestCompleteBindingStoresPureIdentityWithoutActivatingMessageRoute(t *testing.T) {
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	store := &fakeBindingStore{}
	store.row.WorkspaceID = uuidForTest(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	store.row.AgentID = uuidForTest(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	store.row.InstallerUserID = uuidForTest(t, "cccccccc-cccc-cccc-cccc-cccccccccccc")
	store.identityAttempt = identityAttemptForTest(t, store, canonicalCallbackToken, now.Add(time.Minute))
	router := &fakeBindingRouter{}
	service := newBindingServiceForTest(t, store, router, now)

	result, err := service.CompleteBinding(context.Background(), CompleteBindingParams{
		BindingID:     store.identityAttempt.ID,
		BindingMode:   BindingModeIdentity,
		CallbackToken: canonicalCallbackToken,
		Status:        DingTalkBindingCompletionStatus,
		Identity: IdentityBindingResult{
			Status:                  DingTalkBindingTaskStatusSuccess,
			AccountUID:              "24710833",
			AccountOrgID:            "439446171",
			AccountOrganizationName: "Alibaba Group",
			AccountDisplayName:      "Xu Mo",
			AccountAvatarURL:        "https://example.com/avatar.png",
		},
		Message: MessageBindingResult{Status: DingTalkBindingTaskStatusSkipped},
	})
	if err != nil {
		t.Fatalf("CompleteBinding() error = %v", err)
	}
	if result.Status != DingTalkBindingCompletionStatus ||
		result.IdentityBinding.Status != DingTalkBindingTaskStatusSuccess ||
		result.MessageBinding.Status != DingTalkBindingTaskStatusSkipped ||
		result.Binding.DWSIdentity.Status != "active" ||
		result.Binding.MessageRoute.Status != "unbound" {
		t.Fatalf("result = %#v", result)
	}
	if store.row.ID.Valid || store.activated || router.getCalls != 0 || store.identity.DwsUid != "24710833" {
		t.Fatalf("row status=%q router GETs=%d identity=%#v", store.row.Status, router.getCalls, store.identity)
	}
	assertMetricCounter(t, service.metrics, "dingtalk_account_callback_total", map[string]string{"outcome": "success"}, 1)
}

func TestCompleteBindingRecordsMessageFailureWithoutChangingIdentity(t *testing.T) {
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	router := &fakeBindingRouter{}
	service := newBindingServiceForTest(t, store, router, now)

	result, err := service.CompleteBinding(context.Background(), CompleteBindingParams{
		BindingID:     store.row.ID,
		BindingMode:   BindingModeMessage,
		CallbackToken: canonicalCallbackToken,
		Status:        DingTalkBindingCompletionStatus,
		Identity: IdentityBindingResult{
			Status: DingTalkBindingTaskStatusSkipped,
		},
		Message: MessageBindingResult{
			Status: DingTalkBindingTaskStatusFailed,
			Error: &BindingTaskError{
				Code:      "subscription_failed",
				Message:   "unable to create subscription",
				Retryable: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("CompleteBinding() error = %v", err)
	}
	if result.Binding.DWSIdentity.Status != "unbound" ||
		result.Binding.MessageRoute.Status != DingTalkBindingStatusFailed ||
		store.row.Status != "pending" || router.getCalls != 0 {
		t.Fatalf("result=%#v row status=%q router GETs=%d", result, store.row.Status, router.getCalls)
	}
}

func TestCompleteBindingCompletesMessageSubscriptionWithoutExecutionIdentity(t *testing.T) {
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-channel",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Surface:     SubscriptionSurface{Type: DingTalkSurfaceIssue},
		Outbound:    SubscriptionOutbound{Mode: "dws", ReplyTo: "latest_message"},
		Status:      "active",
	}}
	service := newBindingServiceForTest(t, store, router, now)

	result, err := service.CompleteBinding(context.Background(), CompleteBindingParams{
		BindingID:     store.row.ID,
		BindingMode:   BindingModeMessage,
		CallbackToken: canonicalCallbackToken,
		Status:        DingTalkBindingCompletionStatus,
		Identity: IdentityBindingResult{
			Status: DingTalkBindingTaskStatusSkipped,
		},
		Message: MessageBindingResult{
			Status:             DingTalkBindingTaskStatusSuccess,
			AccountDisplayName: "Digital Worker Zhang",
			AccountAvatarURL:   "https://example.com/digital-worker.png",
			MessageScope:       DingTalkMessageScopeDirectOnly,
			SourceID:           "source-channel",
			Subscriptions: []BindingSubscriptionResult{
				{Domain: "channel", SourceID: "source-channel", Status: "active"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CompleteBinding() error = %v", err)
	}
	if result.Binding.DWSIdentity.Status != "unbound" || result.Binding.MessageRoute.Status != "active" ||
		result.Binding.MessageRoute.AccountDisplayName != "Digital Worker Zhang" ||
		result.Binding.MessageRoute.AccountAvatarURL != "https://example.com/digital-worker.png" ||
		result.Binding.MessageRoute.SurfaceType != DingTalkSurfaceIssue ||
		store.row.Status != "active" || store.identity.AgentID.Valid || router.getCalls != 1 {
		t.Fatalf("result=%#v row status=%q router GETs=%d", result, store.row.Status, router.getCalls)
	}
	stored, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccountDisplayName != "Digital Worker Zhang" ||
		stored.AccountAvatarURL != "https://example.com/digital-worker.png" ||
		stored.SurfaceType != DingTalkSurfaceIssue {
		t.Fatalf("stored config = %#v", stored)
	}
}

func TestUpdateDingTalkAccountBindingSurfacePreservesAccountSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	config.RouterSourceID = "source-channel"
	config.AccountDisplayName = "Digital Worker Zhang"
	config.AccountAvatarURL = "https://example.com/digital-worker.png"
	config.SurfaceType = DingTalkSurfaceIssue
	config.MessageScope = DingTalkMessageScopeDirectOnly
	config.BoundAt = &now
	store.row.Config, err = config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store.row.Status = "active"
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-channel",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Surface:     SubscriptionSurface{Type: DingTalkSurfaceIssue},
		Outbound:    SubscriptionOutbound{Mode: "dws", ReplyTo: "latest_message"},
		Status:      "active",
	}}
	service := newBindingServiceForTest(t, store, router, now)

	result, err := service.UpdateSurface(context.Background(), UpdateSurfaceParams{
		WorkspaceID: store.row.WorkspaceID,
		AgentID:     store.row.AgentID,
		SurfaceType: DingTalkSurfaceChat,
	})
	if err != nil {
		t.Fatalf("UpdateSurface() error = %v", err)
	}
	if len(router.updated) != 1 || router.updated[0] != "source-channel:"+uuidStringForTest(store.row.AgentID)+":"+DingTalkSurfaceChat {
		t.Fatalf("router updates = %#v", router.updated)
	}
	if !store.updatedSurface || result.MessageRoute.SurfaceType != DingTalkSurfaceChat ||
		result.MessageRoute.AccountDisplayName != "Digital Worker Zhang" ||
		result.MessageRoute.AccountAvatarURL != "https://example.com/digital-worker.png" ||
		result.MessageRoute.MessageScope != DingTalkMessageScopeDirectOnly {
		t.Fatalf("result = %#v", result)
	}
}

func TestCompleteBindingRejectsMalformedTaskDetails(t *testing.T) {
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		params CompleteBindingParams
	}{
		{
			name: "message mode carries identity failure",
			params: CompleteBindingParams{
				Status: DingTalkBindingCompletionStatus,
				Identity: IdentityBindingResult{
					Status: DingTalkBindingTaskStatusFailed,
					Error:  &BindingTaskError{Code: "identity_failed", Message: "failed"},
				},
				Message: MessageBindingResult{Status: DingTalkBindingTaskStatusFailed, Error: &BindingTaskError{Code: "subscription_failed", Message: "failed"}},
			},
		},
		{
			name: "message mode carries identity success",
			params: CompleteBindingParams{
				Status: DingTalkBindingCompletionStatus,
				Identity: IdentityBindingResult{
					Status:                  DingTalkBindingTaskStatusSuccess,
					AccountUID:              "24710833",
					AccountOrgID:            "439446171",
					AccountOrganizationName: "Alibaba Group",
					AccountDisplayName:      "Xu Mo",
				},
				Message: MessageBindingResult{
					Status:       DingTalkBindingTaskStatusSuccess,
					MessageScope: DingTalkMessageScopeDirectOnly,
					SourceID:     "source-channel",
				},
			},
		},
		{
			name: "subscription source has surrounding whitespace",
			params: CompleteBindingParams{
				Status: DingTalkBindingCompletionStatus,
				Identity: IdentityBindingResult{
					Status: DingTalkBindingTaskStatusSkipped,
				},
				Message: MessageBindingResult{
					Status:       DingTalkBindingTaskStatusSuccess,
					MessageScope: DingTalkMessageScopeDirectOnly,
					SourceID:     "source-channel",
					Subscriptions: []BindingSubscriptionResult{
						{Domain: "channel", SourceID: " source-channel ", Status: "active"},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := pendingBindingStore(t, now, canonicalCallbackToken)
			tt.params.BindingID = store.row.ID
			tt.params.BindingMode = BindingModeMessage
			tt.params.CallbackToken = canonicalCallbackToken
			service := newBindingServiceForTest(t, store, &fakeBindingRouter{}, now)

			_, err := service.CompleteBinding(context.Background(), tt.params)
			if !errors.Is(err, ErrInvalidResult) {
				t.Fatalf("CompleteBinding() error = %v, want ErrInvalidResult", err)
			}
		})
	}
}

func (f *fakeBindingRouter) DeleteSubscription(_ context.Context, sourceID string) error {
	f.deleted = append(f.deleted, sourceID)
	return f.deleteErr
}

func TestBeginDingTalkAccountBindingUsesDispatchPathWithoutPersistingRouterToken(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	workspaceID := uuidForTest(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	agentID := uuidForTest(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	initiatorID := uuidForTest(t, "cccccccc-cccc-cccc-cccc-cccccccccccc")
	oldEndpoint := "v1_EREREREREREREREREREREQ"
	oldConfig := NewPendingDingTalkAccountConfig(
		oldEndpoint,
		"http://legacy-multica.example/api/webhooks/agent-dispatch/"+oldEndpoint,
		HashCallbackToken(canonicalCallbackToken),
		now.Add(time.Minute),
	)
	oldRaw, err := oldConfig.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeBindingStore{row: bindingRow(
		uuidStringForTest(workspaceID),
		uuidStringForTest(agentID),
		"11111111-1111-1111-1111-111111111111",
		"pending",
		oldRaw,
	)}
	router := &fakeBindingRouter{issued: BindingToken{
		BindingToken: "bat_v1.router-secret-must-not-be-persisted",
		ExpiresAt:    now.Add(5 * time.Minute),
	}}
	service := newBindingServiceForTest(t, store, router, now)

	result, err := service.Begin(context.Background(), BeginParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		InitiatorID: initiatorID,
		BindingMode: BindingModeMessage,
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if result.BindingID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("binding id = %q", result.BindingID)
	}
	if result.ExpiresAt != router.issued.ExpiresAt {
		t.Fatalf("expires at = %v, want %v", result.ExpiresAt, router.issued.ExpiresAt)
	}
	parsedQRCodeURL, err := url.Parse(result.QRCodeURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsedQRCodeURL.RawQuery != "" {
		t.Fatalf("QR code secrets leaked into query: %q", parsedQRCodeURL.RawQuery)
	}
	_, rawFragment, found := strings.Cut(result.QRCodeURL, "#")
	if !found {
		t.Fatalf("QR code URL has no fragment: %q", result.QRCodeURL)
	}
	fragment, err := url.ParseQuery(rawFragment)
	if err != nil {
		t.Fatal(err)
	}
	wantDispatchPath := "/api/webhooks/agent-dispatch/" + oldEndpoint
	if len(fragment) != 7 || fragment.Get("bindingMode") != "message" ||
		fragment.Get("bindingToken") != router.issued.BindingToken ||
		fragment.Get("callbackToken") == "" || fragment.Get("callbackUrl") == "" ||
		fragment.Get("expiresAt") != strconv.FormatInt(router.issued.ExpiresAt.Unix(), 10) ||
		fragment.Get("agentId") != uuidStringForTest(store.row.AgentID) ||
		fragment.Get("dispatchPath") != wantDispatchPath {
		t.Fatalf("unexpected QR fragment: %#v", fragment)
	}
	for key, values := range fragment {
		if len(values) != 1 {
			t.Fatalf("QR fragment field %q has %d values", key, len(values))
		}
	}
	if strings.Contains(rawFragment, oldConfig.DispatchURL) ||
		!strings.Contains(rawFragment, "dispatchPath=%2Fapi%2Fwebhooks%2Fagent-dispatch%2F") {
		t.Fatalf("dispatch path was not encoded exactly once: %q", rawFragment)
	}
	wantCallbackURL := "https://multica.example/api/integrations/dingtalk/account-bindings/11111111-1111-1111-1111-111111111111/callback"
	if got := fragment.Get("callbackUrl"); got != wantCallbackURL {
		t.Fatalf("callback URL after one browser decode = %q, want %q", got, wantCallbackURL)
	}
	persisted := string(store.row.Config)
	callbackToken := fragment.Get("callbackToken")
	if strings.Contains(persisted, router.issued.BindingToken) || strings.Contains(persisted, callbackToken) ||
		strings.Contains(string(store.beginConfig), router.issued.BindingToken) ||
		strings.Contains(string(store.beginConfig), callbackToken) {
		t.Fatal("raw binding credential was persisted")
	}
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	if config.DispatchEndpointID != oldEndpoint {
		t.Fatalf("endpoint = %q, want reused %q", config.DispatchEndpointID, oldEndpoint)
	}
	if config.DispatchURL != wantDispatchPath {
		t.Fatalf("persisted dispatch target = %q, want canonical path %q", config.DispatchURL, wantDispatchPath)
	}
	if !VerifyCallbackToken(callbackToken, config.CallbackTokenHash) {
		t.Fatal("persisted callback hash does not match returned token")
	}
	if fragment.Has("identityCallbackToken") || fragment.Has("identityCallbackUrl") {
		t.Fatalf("legacy identity callback fields leaked into QR fragment: %#v", fragment)
	}
	if router.issueAgent != uuidStringForTest(agentID) || router.issueURL != wantDispatchPath {
		t.Fatalf("Router issue request = agent %q path %q", router.issueAgent, router.issueURL)
	}
	assertMetricCounter(t, service.metrics, "dingtalk_account_begin_total", map[string]string{"outcome": "success"}, 1)
}

func TestBeginDingTalkAccountBindingRejectsExpiredRouterToken(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := &fakeBindingStore{}
	router := &fakeBindingRouter{issued: BindingToken{
		BindingToken: "bat_v1.expired-router-token",
		ExpiresAt:    now,
	}}
	service := newBindingServiceForTest(t, store, router, now)

	_, err := service.Begin(context.Background(), BeginParams{
		WorkspaceID: uuidForTest(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
		AgentID:     uuidForTest(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
		InitiatorID: uuidForTest(t, "cccccccc-cccc-cccc-cccc-cccccccccccc"),
		BindingMode: BindingModeMessage,
	})
	if !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("Begin() error = %v, want ErrInvalidResult", err)
	}
}

func TestBeginDingTalkAccountBindingRejectsMismatchedInstallationOwnership(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	workspaceID := uuidForTest(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	agentID := uuidForTest(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	config := NewPendingDingTalkAccountConfig(
		"v1_EREREREREREREREREREREQ",
		"https://multica.example/api/webhooks/agent-dispatch/v1_EREREREREREREREREREREQ",
		HashCallbackToken(canonicalCallbackToken),
		now.Add(time.Minute),
	)
	raw, err := config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		workspaceID string
		agentID     string
	}{
		{
			name:        "different workspace",
			workspaceID: "dddddddd-dddd-dddd-dddd-dddddddddddd",
			agentID:     uuidStringForTest(agentID),
		},
		{
			name:        "different agent",
			workspaceID: uuidStringForTest(workspaceID),
			agentID:     "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeBindingStore{row: bindingRow(
				tt.workspaceID,
				tt.agentID,
				"11111111-1111-1111-1111-111111111111",
				"pending",
				raw,
			)}
			router := &fakeBindingRouter{issued: BindingToken{
				BindingToken: "bat_v1.router-secret-must-not-be-persisted",
				ExpiresAt:    now.Add(5 * time.Minute),
			}}
			service := newBindingServiceForTest(t, store, router, now)
			service.endpoints.store.(*fakeDispatchEndpointStore).record = DispatchEndpoint{
				WorkspaceID: workspaceID,
				AgentID:     agentID,
				ActorUserID: uuidForTest(t, "cccccccc-cccc-cccc-cccc-cccccccccccc"),
				EndpointID:  "v1_EREREREREREREREREREREQ",
				DispatchURL: "https://multica.example/api/webhooks/agent-dispatch/v1_EREREREREREREREREREREQ",
			}

			_, err := service.Begin(context.Background(), BeginParams{
				WorkspaceID: workspaceID,
				AgentID:     agentID,
				InitiatorID: uuidForTest(t, "cccccccc-cccc-cccc-cccc-cccccccccccc"),
				BindingMode: BindingModeMessage,
			})
			if !errors.Is(err, ErrInvalidResult) {
				t.Fatalf("Begin() error = %v, want ErrInvalidResult", err)
			}
			if router.issueAgent != "" {
				t.Fatalf("mismatched installation issued Router token for agent %q", router.issueAgent)
			}
		})
	}
}

func TestBeginDingTalkAccountBindingRejectsActive(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	config := NewPendingDingTalkAccountConfig(
		"v1_EREREREREREREREREREREQ",
		"https://multica.example/api/webhooks/agent-dispatch/v1_EREREREREREREREREREREQ",
		HashCallbackToken(canonicalCallbackToken),
		now.Add(time.Minute),
	)
	raw, err := config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeBindingStore{
		row: bindingRow(
			"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
			"11111111-1111-1111-1111-111111111111",
			"active",
			raw,
		),
		beginErr: pgx.ErrNoRows,
	}
	router := &fakeBindingRouter{}
	service := newBindingServiceForTest(t, store, router, now)

	_, err = service.Begin(context.Background(), BeginParams{
		WorkspaceID: store.row.WorkspaceID,
		AgentID:     store.row.AgentID,
		InitiatorID: uuidForTest(t, "cccccccc-cccc-cccc-cccc-cccccccccccc"),
		BindingMode: BindingModeMessage,
	})
	if !errors.Is(err, ErrAlreadyActive) {
		t.Fatalf("Begin() error = %v, want ErrAlreadyActive", err)
	}
	if router.issueAgent != "" {
		t.Fatal("active binding must not issue a Router token")
	}
}

func TestCompleteCallbackRejectsMissingRouterSourceID(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	callbackToken := canonicalCallbackToken
	store := pendingBindingStore(t, now, callbackToken)
	router := &fakeBindingRouter{}
	service := newBindingServiceForTest(t, store, router, now)

	_, err := service.CompleteCallback(context.Background(), messageCallbackParamsForTest(store.row.ID, ""))
	if !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("CompleteCallback() error = %v, want ErrInvalidResult", err)
	}
	if store.activated {
		t.Fatal("callback without Router source activated binding")
	}
	assertMetricCounter(t, service.metrics, "dingtalk_account_callback_total", map[string]string{"outcome": "invalid_result"}, 1)
}

func TestCompleteCallbackVerifiesSubscriptionAndActivates(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	callbackToken := canonicalCallbackToken
	store := pendingBindingStore(t, now, callbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-1",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Status:      "active",
	}}
	service := newBindingServiceForTest(t, store, router, now)

	params := messageCallbackParamsForTest(store.row.ID, "source-1")
	params.CallbackToken = callbackToken
	params.MessageBinding.MessageScope = DingTalkMessageScopeCustom
	params.MessageBinding.Conversations = []DingTalkConversationSnapshot{
		{
			CID:           "cid-alpha",
			Name:          "Project Alpha",
			AvatarMediaID: "@media-alpha",
			AvatarURL:     "https://example.com/alpha.png",
		},
		{CID: "cid-beta", Name: "Project Beta"},
	}
	binding, err := service.CompleteCallback(context.Background(), params)
	if err != nil {
		t.Fatalf("CompleteCallback() error = %v", err)
	}
	if !store.activated || binding.DWSIdentity.Status != "unbound" || binding.MessageRoute.Status != "active" ||
		binding.MessageRoute.MessageScope != DingTalkMessageScopeCustom || len(binding.MessageRoute.Conversations) != 2 {
		t.Fatalf("binding = %#v, activated=%v", binding, store.activated)
	}
	activeConfig, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	if activeConfig.RouterSourceID != "source-1" || activeConfig.BoundAt == nil ||
		activeConfig.MessageScope != DingTalkMessageScopeCustom || len(activeConfig.Conversations) != 2 ||
		activeConfig.Conversations[0].AvatarMediaID != "@media-alpha" {
		t.Fatalf("active config = %#v", activeConfig)
	}
	assertMetricCounter(t, service.metrics, "dingtalk_account_callback_total", map[string]string{"outcome": "success"}, 1)
	assertMetricCounter(t, service.metrics, "dingtalk_account_subscription_verify_total", map[string]string{"outcome": "success"}, 1)
	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{callbackToken, config.CallbackTokenHash, config.DispatchEndpointID, "source-1"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public binding leaked %q: %s", secret, encoded)
		}
	}
}

func TestCompleteCallbackLegacyPayloadDefaultsToDirectOnly(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-1",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Status:      "active",
	}}
	service := newBindingServiceForTest(t, store, router, now)

	params := messageCallbackParamsForTest(store.row.ID, "source-1")
	params.MessageBinding.MessageScope = ""
	binding, err := service.CompleteCallback(context.Background(), params)
	if err != nil {
		t.Fatalf("CompleteCallback() error = %v", err)
	}
	if binding.MessageRoute.MessageScope != DingTalkMessageScopeDirectOnly || len(binding.MessageRoute.Conversations) != 0 {
		t.Fatalf("legacy callback binding = %#v", binding.MessageRoute)
	}
	activeConfig, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	if activeConfig.MessageScope != DingTalkMessageScopeDirectOnly || len(activeConfig.Conversations) != 0 {
		t.Fatalf("legacy callback config = %#v", activeConfig)
	}
}

func TestCompleteCallbackHasNoConversationCountLimit(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-1",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Status:      "active",
	}}
	service := newBindingServiceForTest(t, store, router, now)
	conversations := make([]DingTalkConversationSnapshot, 400)
	for i := range conversations {
		conversations[i] = DingTalkConversationSnapshot{
			CID:  fmt.Sprintf("cid-%d", i),
			Name: fmt.Sprintf("Conversation %d", i),
		}
	}

	params := messageCallbackParamsForTest(store.row.ID, "source-1")
	params.MessageBinding.MessageScope = DingTalkMessageScopeCustom
	params.MessageBinding.Conversations = conversations
	binding, err := service.CompleteCallback(context.Background(), params)
	if err != nil {
		t.Fatalf("CompleteCallback() error = %v", err)
	}
	if len(binding.MessageRoute.Conversations) != len(conversations) {
		t.Fatalf("conversation count = %d, want %d", len(binding.MessageRoute.Conversations), len(conversations))
	}
}

func TestCompleteCallbackRejectsInvalidConversationSnapshots(t *testing.T) {
	tests := []struct {
		name          string
		messageScope  string
		conversations []DingTalkConversationSnapshot
	}{
		{name: "unsupported scope", messageScope: "workspace"},
		{name: "custom without conversations", messageScope: DingTalkMessageScopeCustom},
		{name: "missing cid", messageScope: DingTalkMessageScopeCustom, conversations: []DingTalkConversationSnapshot{{Name: "Project Alpha"}}},
		{name: "missing name", messageScope: DingTalkMessageScopeCustom, conversations: []DingTalkConversationSnapshot{{CID: "cid-alpha"}}},
		{name: "duplicate cid", messageScope: DingTalkMessageScopeCustom, conversations: []DingTalkConversationSnapshot{{CID: "cid-alpha", Name: "Project Alpha"}, {CID: "cid-alpha", Name: "Project Alpha duplicate"}}},
		{name: "oversized cid", messageScope: DingTalkMessageScopeCustom, conversations: []DingTalkConversationSnapshot{{CID: strings.Repeat("c", 257), Name: "Project Alpha"}}},
		{name: "oversized name", messageScope: DingTalkMessageScopeCustom, conversations: []DingTalkConversationSnapshot{{CID: "cid-alpha", Name: strings.Repeat("会", 257)}}},
		{name: "oversized media id", messageScope: DingTalkMessageScopeCustom, conversations: []DingTalkConversationSnapshot{{CID: "cid-alpha", Name: "Project Alpha", AvatarMediaID: strings.Repeat("m", 1025)}}},
		{name: "insecure avatar url", messageScope: DingTalkMessageScopeCustom, conversations: []DingTalkConversationSnapshot{{CID: "cid-alpha", Name: "Project Alpha", AvatarURL: "http://example.com/alpha.png"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
			store := pendingBindingStore(t, now, canonicalCallbackToken)
			config, err := ParseDingTalkAccountConfig(store.row.Config)
			if err != nil {
				t.Fatal(err)
			}
			router := &fakeBindingRouter{subscription: Subscription{
				SourceID:    "source-1",
				AgentID:     uuidStringForTest(store.row.AgentID),
				DispatchURL: config.DispatchURL,
				Status:      "active",
			}}
			service := newBindingServiceForTest(t, store, router, now)

			params := messageCallbackParamsForTest(store.row.ID, "source-1")
			params.MessageBinding.MessageScope = tt.messageScope
			params.MessageBinding.Conversations = tt.conversations
			_, err = service.CompleteCallback(context.Background(), params)
			if !errors.Is(err, ErrInvalidResult) {
				t.Fatalf("CompleteCallback() error = %v, want ErrInvalidResult", err)
			}
			if store.activated {
				t.Fatal("invalid conversation snapshot activated binding")
			}
		})
	}
}

func TestCompleteCallbackMismatchDeletesRouterSubscriptionWithoutActivating(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-1",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Status:      "inactive",
	}}
	service := newBindingServiceForTest(t, store, router, now)

	_, err = service.CompleteCallback(context.Background(), messageCallbackParamsForTest(store.row.ID, "source-1"))
	if !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("CompleteCallback() error = %v, want ErrBindingConflict", err)
	}
	if store.activated {
		t.Fatal("mismatched Router subscription was activated")
	}
	if len(router.deleted) != 1 || router.deleted[0] != "source-1" {
		t.Fatalf("compensation deletes = %#v", router.deleted)
	}
	assertMetricCounter(t, service.metrics, "dingtalk_account_callback_total", map[string]string{"outcome": "conflict"}, 1)
	assertMetricCounter(t, service.metrics, "dingtalk_account_subscription_verify_total", map[string]string{"outcome": "inactive"}, 1)
}

func TestCompleteCallbackRejectsTamperedRoutingWithoutDeleting(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		mutate       func(*Subscription)
		metricResult string
	}{
		{
			name: "agent id",
			mutate: func(subscription *Subscription) {
				subscription.AgentID = "ffffffff-ffff-ffff-ffff-ffffffffffff"
			},
			metricResult: "agent_mismatch",
		},
		{
			name: "dispatch url",
			mutate: func(subscription *Subscription) {
				subscription.DispatchURL = "https://attacker.example/api/webhooks/agent-dispatch/v1_EREREREREREREREREREREQ"
			},
			metricResult: "dispatch_url_mismatch",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := pendingBindingStore(t, now, canonicalCallbackToken)
			config, err := ParseDingTalkAccountConfig(store.row.Config)
			if err != nil {
				t.Fatal(err)
			}
			subscription := Subscription{
				SourceID:    "source-1",
				AgentID:     uuidStringForTest(store.row.AgentID),
				DispatchURL: config.DispatchURL,
				Status:      "active",
			}
			tt.mutate(&subscription)
			router := &fakeBindingRouter{subscription: subscription}
			service := newBindingServiceForTest(t, store, router, now)

			_, err = service.CompleteCallback(context.Background(), messageCallbackParamsForTest(store.row.ID, "source-1"))
			if !errors.Is(err, ErrBindingConflict) {
				t.Fatalf("CompleteCallback() error = %v, want ErrBindingConflict", err)
			}
			if store.activated {
				t.Fatal("tampered Router subscription was activated")
			}
			if len(router.deleted) != 0 {
				t.Fatalf("unverified subscription was deleted: %#v", router.deleted)
			}
			assertMetricCounter(t, service.metrics, "dingtalk_account_subscription_verify_total", map[string]string{"outcome": tt.metricResult}, 1)
		})
	}
}

func TestCompleteCallbackCompensatesVerifiedSourceWhenActivationLosesPendingCAS(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	store.activateErr = pgx.ErrNoRows
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-1",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Status:      "active",
	}}
	service := newBindingServiceForTest(t, store, router, now)

	_, err = service.CompleteCallback(context.Background(), messageCallbackParamsForTest(store.row.ID, "source-1"))
	if !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("CompleteCallback() error = %v, want ErrBindingConflict", err)
	}
	if len(router.deleted) != 1 || router.deleted[0] != "source-1" {
		t.Fatalf("verified stale source compensation = %#v", router.deleted)
	}
}

func TestCompleteCallbackCompensatesLosingSourceWhenAnotherCallbackActivates(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	store.activateErr = pgx.ErrNoRows
	store.activateHook = func(store *fakeBindingStore) {
		winning := config
		winning.RouterSourceID = "source-winner"
		winning.BoundAt = &now
		store.row.Config, err = winning.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		store.row.Status = "active"
	}
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-loser",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Status:      "active",
	}}
	service := newBindingServiceForTest(t, store, router, now)

	_, err = service.CompleteCallback(context.Background(), messageCallbackParamsForTest(store.row.ID, "source-loser"))

	if !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("CompleteCallback() error = %v, want ErrBindingConflict", err)
	}
	if len(router.deleted) != 1 || router.deleted[0] != "source-loser" {
		t.Fatalf("losing source compensation = %#v", router.deleted)
	}
}

func TestCompleteCallbackIsIdempotentAndRejectsDifferentSource(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	boundAt := now.Add(-time.Second)
	config.RouterSourceID = "source-1"
	config.BoundAt = &boundAt
	store.row.Config, err = config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store.row.Status = "active"
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-1",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Status:      "active",
	}}
	service := newBindingServiceForTest(t, store, router, now)

	if _, err := service.CompleteCallback(context.Background(), messageCallbackParamsForTest(store.row.ID, "source-1")); err != nil {
		t.Fatalf("idempotent callback error = %v", err)
	}
	if store.activated {
		t.Fatal("idempotent callback must not rewrite active row")
	}

	_, err = service.CompleteCallback(context.Background(), messageCallbackParamsForTest(store.row.ID, "source-2"))
	if !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("different-source callback error = %v", err)
	}
	if len(router.deleted) != 0 {
		t.Fatalf("different source must not delete an unverified Router subscription: %#v", router.deleted)
	}
}

func TestCompleteCallbackRejectsExpiredTokenBeforeRouterLookup(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now.Add(-20*time.Minute), canonicalCallbackToken)
	router := &fakeBindingRouter{}
	service := newBindingServiceForTest(t, store, router, now)

	_, err := service.CompleteCallback(context.Background(), messageCallbackParamsForTest(store.row.ID, "source-1"))
	if !errors.Is(err, ErrCallbackExpired) {
		t.Fatalf("CompleteCallback() error = %v, want ErrCallbackExpired", err)
	}
	if router.subscription.SourceID != "" {
		t.Fatal("expired callback should not query Router")
	}
}

func TestCompleteCallbackMapsMissingRouterSubscriptionToConflict(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	router := &fakeBindingRouter{getErr: ErrSubscriptionNotFound}
	service := newBindingServiceForTest(t, store, router, now)

	_, err := service.CompleteCallback(context.Background(), messageCallbackParamsForTest(store.row.ID, "source-1"))
	if !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("CompleteCallback() error = %v, want ErrBindingConflict", err)
	}
}

func TestCompleteCallbackRejectsOversizedSourceID(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	router := &fakeBindingRouter{}
	service := newBindingServiceForTest(t, store, router, now)

	_, err := service.CompleteCallback(context.Background(), messageCallbackParamsForTest(store.row.ID, strings.Repeat("s", 65)))
	if !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("CompleteCallback() error = %v, want ErrInvalidResult", err)
	}
}

func TestCompleteCallbackIdentityModeValidatesAndStoresIdentity(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	identityToken := canonicalCallbackToken
	store.identityAttempt = identityAttemptForTest(t, store, identityToken, now.Add(10*time.Minute))
	service := newBindingServiceForTest(t, store, &fakeBindingRouter{}, now)

	params := identityCallbackParamsForTest(store.identityAttempt.ID)
	params.CallbackToken = identityToken
	binding, err := service.CompleteCallback(context.Background(), params)
	if err != nil {
		t.Fatalf("CompleteCallback(identity) error = %v", err)
	}
	if binding.DWSIdentity.Status != "active" ||
		binding.DWSIdentity.OrganizationName != "Alibaba Group" ||
		binding.DWSIdentity.AccountDisplayName != "Xu Mo" ||
		binding.MessageRoute.Status != "pending" {
		t.Fatalf("binding = %#v", binding)
	}
	if store.identity.DwsUid != "24710833" || store.identity.OrgID != "439446171" ||
		store.identity.OrganizationName != "Alibaba Group" {
		t.Fatalf("stored identity = %#v", store.identity)
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"24710833", "439446171", identityToken} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public binding leaked identity value %q: %s", secret, encoded)
		}
	}

	if _, err := service.CompleteCallback(context.Background(), params); err != nil {
		t.Fatalf("idempotent identity callback error = %v", err)
	}
}

func TestCompleteCallbackIdentityModeRejectsInvalidBackendUID(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	store.identityAttempt = identityAttemptForTest(t, store, canonicalCallbackToken, now.Add(time.Minute))
	service := newBindingServiceForTest(t, store, &fakeBindingRouter{}, now)
	params := identityCallbackParamsForTest(store.identityAttempt.ID)
	params.IdentityBinding.AccountUID = "not-a-uid"

	_, err := service.CompleteCallback(context.Background(), params)
	if !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("CompleteCallback(identity) error = %v, want ErrInvalidResult", err)
	}
	if store.identity.AgentID.Valid {
		t.Fatalf("mismatched identity was persisted: %#v", store.identity)
	}
}

func TestCompleteCallbackIdentityModeRejectsExpiredAttempt(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	store.identityAttempt = identityAttemptForTest(t, store, canonicalCallbackToken, now.Add(-time.Second))
	service := newBindingServiceForTest(t, store, &fakeBindingRouter{}, now)

	_, err := service.CompleteCallback(context.Background(), identityCallbackParamsForTest(store.identityAttempt.ID))
	if !errors.Is(err, ErrCallbackExpired) {
		t.Fatalf("CompleteCallback(identity) error = %v, want ErrCallbackExpired", err)
	}
}

func TestCompleteCallbackIdentityModeAllowsSameIdentityForMultipleAgents(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	for i, agentID := range []string{
		"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		"dddddddd-dddd-dddd-dddd-dddddddddddd",
	} {
		store := pendingBindingStore(t, now, canonicalCallbackToken)
		store.row.AgentID = uuidForTest(t, agentID)
		store.identityAttempt = identityAttemptForTest(t, store, canonicalCallbackToken, now.Add(time.Minute))
		service := newBindingServiceForTest(t, store, &fakeBindingRouter{}, now)
		if _, err := service.CompleteCallback(context.Background(), identityCallbackParamsForTest(store.identityAttempt.ID)); err != nil {
			t.Fatalf("agent %d identity callback error = %v", i, err)
		}
		if store.identity.DwsUid != "24710833" || store.identity.AgentID != store.row.AgentID {
			t.Fatalf("agent %d stored identity = %#v", i, store.identity)
		}
	}
}

func TestUnbindDeletesRouterBeforeRevokingAndListIsPublic(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	boundAt := now
	config.RouterSourceID = "source-1"
	config.BoundAt = &boundAt
	config.AccountDisplayName = "Zhang San"
	config.AccountAvatarURL = "https://example.com/avatar.png"
	config.MessageScope = DingTalkMessageScopeCustom
	config.Conversations = []DingTalkConversationSnapshot{
		{CID: "cid-alpha", Name: "Project Alpha", AvatarMediaID: "@media-alpha", AvatarURL: "https://example.com/alpha.png"},
	}
	store.row.Config, err = config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store.row.Status = "active"
	setIdentityForTest(store)
	router := &fakeBindingRouter{}
	service := newBindingServiceForTest(t, store, router, now)

	binding, err := service.Unbind(context.Background(), UnbindParams{
		WorkspaceID: store.row.WorkspaceID,
		AgentID:     store.row.AgentID,
		BindingMode: BindingModeMessage,
	})
	if err != nil {
		t.Fatalf("Unbind() error = %v", err)
	}
	if len(router.deleted) != 1 || router.deleted[0] != "source-1" || !store.revoked ||
		binding.MessageRoute.Status != "revoked" || binding.DWSIdentity.Status != "active" ||
		binding.DWSIdentity.Source != "identity" || !store.identity.AgentID.Valid {
		t.Fatalf("delete=%#v revoked=%v binding=%#v", router.deleted, store.revoked, binding)
	}
	if store.revokeArg.AgentID != store.row.AgentID {
		t.Fatalf("revoke agent = %v, want %v", store.revokeArg.AgentID, store.row.AgentID)
	}
	var revokedConfig map[string]any
	if err := json.Unmarshal(store.row.Config, &revokedConfig); err != nil {
		t.Fatal(err)
	}
	for _, cleared := range []string{
		"callback_token_hash",
		"callback_expires_at",
		"router_source_id",
		"account_display_name",
		"account_avatar_url",
		"surface_type",
		"message_scope",
		"conversations",
		"bound_at",
	} {
		if _, exists := revokedConfig[cleared]; exists {
			t.Fatalf("revoked config retained %q: %#v", cleared, revokedConfig)
		}
	}
	for key, want := range map[string]any{
		"schema_version":       float64(config.SchemaVersion),
		"dispatch_endpoint_id": config.DispatchEndpointID,
		"dispatch_key_id":      config.DispatchKeyID,
		"dispatch_url":         config.DispatchURL,
	} {
		if revokedConfig[key] != want {
			t.Fatalf("revoked config %s = %#v, want %#v", key, revokedConfig[key], want)
		}
	}
	assertMetricCounter(t, service.metrics, "dingtalk_account_unbind_total", map[string]string{"outcome": "success"}, 1)

	listed, err := service.List(context.Background(), store.row.WorkspaceID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 || listed[0].MessageRoute.Status != "revoked" ||
		listed[0].DWSIdentity.Status != "active" || listed[0].DWSIdentity.Source != "identity" {
		t.Fatalf("listed = %#v", listed)
	}
	encoded, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{config.RouterSourceID, config.CallbackTokenHash, config.DispatchEndpointID} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("list leaked %q: %s", secret, encoded)
		}
	}
}

func TestUnbindPendingMessageKeepsIndependentIdentity(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	setIdentityForTest(store)
	service := newBindingServiceForTest(t, store, &fakeBindingRouter{}, now)

	binding, err := service.Unbind(context.Background(), UnbindParams{
		WorkspaceID: store.row.WorkspaceID,
		AgentID:     store.row.AgentID,
		BindingMode: BindingModeMessage,
	})
	if err != nil {
		t.Fatalf("Unbind(pending message) error = %v", err)
	}
	if !store.identity.AgentID.Valid || binding.DWSIdentity.Status != "active" ||
		binding.DWSIdentity.Source != "identity" || binding.MessageRoute.Status != "revoked" {
		t.Fatalf("independent identity was not preserved: store=%#v binding=%#v", store.identity, binding)
	}
}

func TestUnbindIdentityKeepsActiveMessageRoute(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	store := activeBindingStoreForUnbind(t, now)
	setIdentityForTest(store)
	service := newBindingServiceForTest(t, store, &fakeBindingRouter{}, now)

	binding, err := service.Unbind(context.Background(), UnbindParams{
		WorkspaceID: store.row.WorkspaceID,
		AgentID:     store.row.AgentID,
		BindingMode: BindingModeIdentity,
	})
	if err != nil {
		t.Fatalf("Unbind(identity with active message route) error = %v", err)
	}
	if store.identity.AgentID.Valid || store.row.Status != "active" ||
		binding.DWSIdentity.Status != "unbound" || binding.MessageRoute.Status != "active" {
		t.Fatalf("identity=%#v row=%#v binding=%#v", store.identity, store.row, binding)
	}
}

// activeBindingStoreForUnbind builds an ACTIVE binding row with a router
// source id, the precondition every unbind test starts from.
func activeBindingStoreForUnbind(t *testing.T, now time.Time) *fakeBindingStore {
	t.Helper()
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	boundAt := now
	config.RouterSourceID = "source-1"
	config.BoundAt = &boundAt
	store.row.Config, err = config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store.row.Status = "active"
	return store
}

func TestUnbindProceedsWhenRouterSubscriptionAlreadyGone(t *testing.T) {
	// Multi-replica recovery: a crash between the router delete and the
	// local revoke (or a concurrent unbind on another pod) leaves the
	// subscription already deleted remotely. The router reports that as its
	// subscription_not_found business error — the retry must treat it as
	// "already deleted" and complete the local revoke, not 502 forever.
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := activeBindingStoreForUnbind(t, now)
	router := &fakeBindingRouter{deleteErr: newRouterAPIError("business_error", "subscription_not_found")}
	service := newBindingServiceForTest(t, store, router, now)

	binding, err := service.Unbind(context.Background(), UnbindParams{
		WorkspaceID: store.row.WorkspaceID,
		AgentID:     store.row.AgentID,
		BindingMode: BindingModeMessage,
	})
	if err != nil {
		t.Fatalf("Unbind() error = %v, want success on already-deleted subscription", err)
	}
	if !store.revoked || binding.MessageRoute.Status != "revoked" {
		t.Fatalf("revoked=%v binding=%#v", store.revoked, binding)
	}
	assertMetricCounter(t, service.metrics, "dingtalk_account_unbind_total", map[string]string{"outcome": "success"}, 1)
}

func TestUnbindStaysFailClosedOnOtherRouterErrors(t *testing.T) {
	// Anything but the explicit not-found signal (transport failure, 5xx,
	// auth error) must still abort BEFORE the local transition, or a live
	// router subscription would keep dispatching into a revoked binding.
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := activeBindingStoreForUnbind(t, now)
	router := &fakeBindingRouter{deleteErr: errors.New("router transport failure")}
	service := newBindingServiceForTest(t, store, router, now)

	_, err := service.Unbind(context.Background(), UnbindParams{
		WorkspaceID: store.row.WorkspaceID,
		AgentID:     store.row.AgentID,
		BindingMode: BindingModeMessage,
	})
	if !errors.Is(err, ErrRouterUnavailable) {
		t.Fatalf("Unbind() error = %v, want ErrRouterUnavailable", err)
	}
	if store.revoked {
		t.Fatal("local row must not be revoked when the router delete failed")
	}
}

func TestListClearsExpiredCallbackCredentialBeforeReturningBindings(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 20, 0, 0, time.UTC)
	store := pendingBindingStore(t, now.Add(-20*time.Minute), canonicalCallbackToken)
	router := &fakeBindingRouter{}
	service := newBindingServiceForTest(t, store, router, now)

	listed, err := service.List(context.Background(), store.row.WorkspaceID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed = %#v", listed)
	}
	if store.cleanupCalls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", store.cleanupCalls)
	}
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	if config.CallbackTokenHash != "" || !config.CallbackExpiresAt.IsZero() {
		t.Fatalf("expired callback credential was retained: %#v", config)
	}
}

func TestListLoadsCurrentSurfaceForExistingActiveBinding(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	store := activeBindingStoreForUnbind(t, now)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    config.RouterSourceID,
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Surface:     SubscriptionSurface{Type: DingTalkSurfaceChat},
		Outbound:    SubscriptionOutbound{Mode: "dws", ReplyTo: "latest_message"},
		Status:      "active",
	}}
	service := newBindingServiceForTest(t, store, router, now)

	listed, err := service.List(context.Background(), store.row.WorkspaceID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 || listed[0].MessageRoute.SurfaceType != DingTalkSurfaceChat || router.getCalls != 1 {
		t.Fatalf("listed = %#v router GETs = %d", listed, router.getCalls)
	}
}

func newBindingServiceForTest(t *testing.T, store *fakeBindingStore, router Router, now time.Time) *Service {
	t.Helper()
	keyring, err := ParseDispatchKeyring("v1:ERERERERERERERERERERERERERERERERERERERERERE", "v1")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, router, ServiceConfig{
		PublicBaseURL:   "https://multica.example",
		DBaseBindingURL: "https://dbase.example/bind",
		CallbackTTL:     10 * time.Minute,
		Keyring:         keyring,
		Random:          rand.Reader,
		Now:             func() time.Time { return now },
		IdentityStore:   store,
		Endpoints:       newBindingEndpointServiceForTest(t, store, keyring),
		Metrics:         obsmetrics.NewBusinessMetrics(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newBindingEndpointServiceForTest(t *testing.T, store *fakeBindingStore, keyring *DispatchKeyring) *DispatchEndpointService {
	t.Helper()
	endpointStore := &fakeDispatchEndpointStore{}
	if store.row.ID.Valid {
		config, err := ParseDingTalkAccountConfig(store.row.Config)
		if err == nil {
			actorUserID := store.row.InstallerUserID
			if !actorUserID.Valid {
				actorUserID = mustUUIDForTest("cccccccc-cccc-cccc-cccc-cccccccccccc")
			}
			endpointStore.record = DispatchEndpoint{
				WorkspaceID: store.row.WorkspaceID,
				AgentID:     store.row.AgentID,
				ActorUserID: actorUserID,
				EndpointID:  config.DispatchEndpointID,
				DispatchURL: config.DispatchURL,
			}
		}
	}
	service, err := NewDispatchEndpointService(endpointStore, DispatchEndpointServiceConfig{
		PublicBaseURL: "https://multica.example",
		Keyring:       keyring,
		Random:        bytes.NewReader(bytes.Repeat([]byte{0x33}, 4096)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertMetricCounter(t *testing.T, businessMetrics *obsmetrics.BusinessMetrics, name string, labels map[string]string, want float64) {
	t.Helper()
	family := obsmetrics.GatherForTest(t, businessMetrics)[name]
	if family == nil {
		t.Fatalf("metric family %s is missing", name)
	}
	for _, metric := range family.GetMetric() {
		if metricLabelsMatch(metric, labels) {
			if got := metric.GetCounter().GetValue(); got != want {
				t.Fatalf("metric %s = %v, want %v", name, got, want)
			}
			return
		}
	}
	t.Fatalf("metric %s labels %#v are missing", name, labels)
}

func metricLabelsMatch(metric *dto.Metric, labels map[string]string) bool {
	if len(metric.GetLabel()) != len(labels) {
		return false
	}
	for _, label := range metric.GetLabel() {
		if labels[label.GetName()] != label.GetValue() {
			return false
		}
	}
	return true
}

func pendingBindingStore(t *testing.T, now time.Time, callbackToken string) *fakeBindingStore {
	t.Helper()
	endpoint := "v1_EREREREREREREREREREREQ"
	config := NewPendingDingTalkAccountConfig(
		endpoint,
		"https://multica.example/api/webhooks/agent-dispatch/"+endpoint,
		HashCallbackToken(callbackToken),
		now.Add(10*time.Minute),
	)
	raw, err := config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return &fakeBindingStore{row: bindingRow(
		"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		"11111111-1111-1111-1111-111111111111",
		"pending",
		raw,
	)}
}

func identityAttemptForTest(t *testing.T, store *fakeBindingStore, callbackToken string, expiresAt time.Time) db.AgentDingtalkIdentityAttempt {
	t.Helper()
	return db.AgentDingtalkIdentityAttempt{
		ID:                uuidForTest(t, "22222222-2222-2222-2222-222222222222"),
		WorkspaceID:       store.row.WorkspaceID,
		AgentID:           store.row.AgentID,
		InitiatorUserID:   store.row.InstallerUserID,
		CallbackTokenHash: HashCallbackToken(callbackToken),
		ExpiresAt:         pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}
}

func messageCallbackParamsForTest(bindingID pgtype.UUID, sourceID string) CallbackParams {
	return CallbackParams{
		BindingID:     bindingID,
		BindingMode:   BindingModeMessage,
		CallbackToken: canonicalCallbackToken,
		IdentityBinding: IdentityBindingResult{
			Status: "skipped",
		},
		MessageBinding: MessageBindingResult{Status: "success", SourceID: sourceID},
	}
}

func identityCallbackParamsForTest(attemptID pgtype.UUID) CallbackParams {
	return CallbackParams{
		BindingID:     attemptID,
		BindingMode:   BindingModeIdentity,
		CallbackToken: canonicalCallbackToken,
		IdentityBinding: IdentityBindingResult{
			Status:                  "success",
			AccountUID:              "24710833",
			AccountOrgID:            "439446171",
			AccountOrganizationName: "Alibaba Group",
			AccountDisplayName:      "Xu Mo",
			AccountAvatarURL:        "https://example.com/avatar.png",
		},
		MessageBinding: MessageBindingResult{Status: "skipped"},
	}
}

func setIdentityForTest(store *fakeBindingStore) {
	now := time.Now().UTC()
	store.identity = db.AgentDingtalkIdentity{
		AgentID:            store.row.AgentID,
		WorkspaceID:        store.row.WorkspaceID,
		DwsUid:             "24710833",
		OrgID:              "439446171",
		OrganizationName:   "Alibaba Group",
		AccountDisplayName: "Xu Mo",
		AccountAvatarUrl:   "https://example.com/avatar.png",
		BoundAt:            pgtype.Timestamptz{Time: now, Valid: true},
	}
}

func bindingRow(workspaceID, agentID, installationID, status string, config []byte) db.ChannelInstallation {
	return db.ChannelInstallation{
		ID:          mustUUIDForTest(installationID),
		WorkspaceID: mustUUIDForTest(workspaceID),
		AgentID:     mustUUIDForTest(agentID),
		ChannelType: ChannelTypeDingTalkAccount,
		Config:      append([]byte(nil), config...),
		Status:      status,
	}
}

func uuidForTest(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	value := mustUUIDForTest(raw)
	if !value.Valid {
		t.Fatalf("invalid uuid %q", raw)
	}
	return value
}

func mustUUIDForTest(raw string) pgtype.UUID {
	var value pgtype.UUID
	_ = value.Scan(raw)
	return value
}

func uuidStringForTest(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return value.String()
}
