package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeInstallationQueries struct {
	row    db.ChannelInstallation
	err    error
	params []db.GetChannelInstallationParams
}

func (f *fakeInstallationQueries) GetChannelInstallation(_ context.Context, params db.GetChannelInstallationParams) (db.ChannelInstallation, error) {
	f.params = append(f.params, params)
	return f.row, f.err
}

func TestInstallationResolverUsesAdmissionInstallationFence(t *testing.T) {
	installationID := util.MustParseUUID("00000000-0000-0000-0000-000000000020")
	config, err := json.Marshal(dingtalkInstallConfig{AppID: "client-1"})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	q := &fakeInstallationQueries{row: db.ChannelInstallation{
		ID:              installationID,
		WorkspaceID:     util.MustParseUUID("00000000-0000-0000-0000-000000000021"),
		AgentID:         util.MustParseUUID("00000000-0000-0000-0000-000000000022"),
		InstallerUserID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		Status:          "active",
		Config:          config,
	}}
	raw, err := json.Marshal(dingtalkRawEvent{
		ClientID:       "client-1",
		InstallationID: util.UUIDToString(installationID),
	})
	if err != nil {
		t.Fatalf("marshal raw event: %v", err)
	}

	resolved, err := (&installationResolver{q: q}).ResolveInstallation(context.Background(), channel.InboundMessage{Raw: raw})
	if err != nil {
		t.Fatalf("ResolveInstallation: %v", err)
	}
	if resolved.ID != installationID || len(q.params) != 1 || q.params[0].ID != installationID {
		t.Fatalf("resolved = %+v, params = %+v", resolved, q.params)
	}
}

func TestInstallationResolverRejectsReclaimedAppIDOwner(t *testing.T) {
	oldInstallationID := util.MustParseUUID("00000000-0000-0000-0000-000000000020")
	config, err := json.Marshal(dingtalkInstallConfig{AppID: "different-client"})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	q := &fakeInstallationQueries{row: db.ChannelInstallation{
		ID:     oldInstallationID,
		Status: "active",
		Config: config,
	}}
	raw, err := json.Marshal(dingtalkRawEvent{
		ClientID:       "client-1",
		InstallationID: util.UUIDToString(oldInstallationID),
	})
	if err != nil {
		t.Fatalf("marshal raw event: %v", err)
	}

	_, err = (&installationResolver{q: q}).ResolveInstallation(context.Background(), channel.InboundMessage{Raw: raw})
	if !errors.Is(err, engine.ErrInstallationNotFound) {
		t.Fatalf("ResolveInstallation error = %v, want ErrInstallationNotFound", err)
	}
}
