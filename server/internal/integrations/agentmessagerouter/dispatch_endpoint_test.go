package agentmessagerouter

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

type fakeDispatchEndpointStore struct {
	record      DispatchEndpoint
	ensureCalls int
	err         error
}

func (f *fakeDispatchEndpointStore) EnsureAgentDispatchEndpoint(_ context.Context, candidate DispatchEndpoint) (DispatchEndpoint, error) {
	f.ensureCalls++
	if f.err != nil {
		return DispatchEndpoint{}, f.err
	}
	if f.record.AgentID.Valid {
		return f.record, nil
	}
	f.record = candidate
	return candidate, nil
}

func (f *fakeDispatchEndpointStore) GetAgentDispatchEndpoint(_ context.Context, agentID pgtype.UUID) (DispatchEndpoint, error) {
	if f.err != nil {
		return DispatchEndpoint{}, f.err
	}
	if !f.record.AgentID.Valid || f.record.AgentID != agentID {
		return DispatchEndpoint{}, pgx.ErrNoRows
	}
	return f.record, nil
}

func TestDispatchEndpointServiceReusesOneEndpointPerAgent(t *testing.T) {
	keyring, err := ParseDispatchKeyring(
		"v1:ERERERERERERERERERERERERERERERERERERERERERE", "v1")
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeDispatchEndpointStore{}
	service, err := NewDispatchEndpointService(store, DispatchEndpointServiceConfig{
		PublicBaseURL: "https://multica.example",
		Keyring:       keyring,
		Random:        bytes.NewReader(bytes.Repeat([]byte{0x11}, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := util.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	agentID := util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	first, err := service.Ensure(context.Background(), workspaceID, agentID,
		util.MustParseUUID("cccccccc-cccc-cccc-cccc-cccccccccccc"))
	if err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	second, err := service.Ensure(context.Background(), workspaceID, agentID,
		util.MustParseUUID("dddddddd-dddd-dddd-dddd-dddddddddddd"))
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if first != second {
		t.Fatalf("endpoint rotated for the same agent: first=%#v second=%#v", first, second)
	}
	if store.ensureCalls != 2 || first.AgentID != agentID || first.WorkspaceID != workspaceID {
		t.Fatalf("stored endpoint = %#v, calls=%d", first, store.ensureCalls)
	}
	wantURL := "https://multica.example/api/webhooks/agent-dispatch/" + first.EndpointID
	if first.DispatchURL != wantURL {
		t.Fatalf("dispatch URL = %q, want %q", first.DispatchURL, wantURL)
	}
}

func TestDispatchEndpointServiceFailsClosedOnInvalidOwnershipAndStorage(t *testing.T) {
	keyring, err := ParseDispatchKeyring(
		"v1:ERERERERERERERERERERERERERERERERERERERERERE", "v1")
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeDispatchEndpointStore{err: errors.New("database unavailable")}
	service, err := NewDispatchEndpointService(store, DispatchEndpointServiceConfig{
		PublicBaseURL: "https://multica.example",
		Keyring:       keyring,
		Random:        bytes.NewReader(bytes.Repeat([]byte{0x22}, 16)),
	})
	if err != nil {
		t.Fatal(err)
	}
	valid := util.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	if _, err := service.Ensure(context.Background(), pgtype.UUID{}, valid, valid); err == nil {
		t.Fatal("expected invalid ownership to fail")
	}
	if _, err := service.Ensure(context.Background(), valid, valid, valid); err == nil ||
		!errors.Is(err, store.err) {
		t.Fatalf("storage error = %v", err)
	}
}

func TestDispatchEndpointServiceRejectsStoredEndpointOutsideConfiguredOrigin(t *testing.T) {
	keyring, err := ParseDispatchKeyring(
		"v1:ERERERERERERERERERERERERERERERERERERERERERE", "v1")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := util.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	agentID := util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	actorID := util.MustParseUUID("cccccccc-cccc-cccc-cccc-cccccccccccc")
	store := &fakeDispatchEndpointStore{record: DispatchEndpoint{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		ActorUserID: actorID,
		EndpointID:  "v1_EREREREREREREREREREREQ",
		DispatchURL: "https://attacker.example/api/webhooks/agent-dispatch/v1_EREREREREREREREREREREQ",
	}}
	service, err := NewDispatchEndpointService(store, DispatchEndpointServiceConfig{
		PublicBaseURL: "https://multica.example",
		Keyring:       keyring,
		Random:        bytes.NewReader(bytes.Repeat([]byte{0x44}, 16)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ensure(context.Background(), workspaceID, agentID, actorID); err == nil {
		t.Fatal("expected a stored endpoint outside the configured Multica origin to fail")
	}
}
