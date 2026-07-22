package dingtalk

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func installerID() pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4}, Valid: true}
}

func resolvedInstallation(t *testing.T, allowUnbound bool, installer pgtype.UUID) engine.ResolvedInstallation {
	t.Helper()
	cfg, err := encodeInstallConfig(Installation{ClientID: "app-1", RobotCode: "robot-1", AllowUnbound: allowUnbound})
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	return engine.ResolvedInstallation{
		ID:              pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
		InstallerUserID: installer,
		Platform:        db.ChannelInstallation{Config: cfg},
	}
}

func TestInstallationAllowsUnbound(t *testing.T) {
	if installationAllowsUnbound(resolvedInstallation(t, false, installerID())) {
		t.Fatal("allow_unbound=false should report false")
	}
	if !installationAllowsUnbound(resolvedInstallation(t, true, installerID())) {
		t.Fatal("allow_unbound=true should report true")
	}
	// Non-dingtalk platform payload → false, never panics.
	if installationAllowsUnbound(engine.ResolvedInstallation{Platform: "not a row"}) {
		t.Fatal("unexpected platform type should report false")
	}
}

func TestApplyAllowUnbound(t *testing.T) {
	r := &identityResolver{}
	installer := installerID()
	boundUser := pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	bound := engine.ResolvedIdentity{PrincipalUserID: boundUser, InitiatorUserID: boundUser}
	otherErr := errors.New("db exploded")

	cases := []struct {
		name          string
		allowUnbound  bool
		installer     pgtype.UUID
		inID          engine.ResolvedIdentity
		inErr         error
		wantPrincipal pgtype.UUID
		wantInitiator pgtype.UUID
		wantErr       error
	}{
		{"success passes through untouched", true, installer, bound, nil, boundUser, boundUser, nil},
		{"unbound + allow uses installer only as principal", true, installer, engine.ResolvedIdentity{}, engine.ErrSenderUnbound, installer, pgtype.UUID{}, nil},
		{"not-member + allow uses installer only as principal", true, installer, engine.ResolvedIdentity{}, engine.ErrSenderNotMember, installer, pgtype.UUID{}, nil},
		{"unbound + no allow → error", false, installer, engine.ResolvedIdentity{}, engine.ErrSenderUnbound, pgtype.UUID{}, pgtype.UUID{}, engine.ErrSenderUnbound},
		{"not-member + no allow → error", false, installer, engine.ResolvedIdentity{}, engine.ErrSenderNotMember, pgtype.UUID{}, pgtype.UUID{}, engine.ErrSenderNotMember},
		{"other error never overridden", true, installer, engine.ResolvedIdentity{}, otherErr, pgtype.UUID{}, pgtype.UUID{}, otherErr},
		{"allow but no installer → error", true, pgtype.UUID{}, engine.ResolvedIdentity{}, engine.ErrSenderUnbound, pgtype.UUID{}, pgtype.UUID{}, engine.ErrSenderUnbound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := resolvedInstallation(t, tc.allowUnbound, tc.installer)
			gotID, gotErr := r.applyAllowUnbound(context.Background(), inst, channel.InboundMessage{
				Source: channel.Source{SenderID: "staff-1"},
			}, tc.inID, tc.inErr)
			if tc.wantErr != nil {
				if !errors.Is(gotErr, tc.wantErr) {
					t.Fatalf("err = %v, want %v", gotErr, tc.wantErr)
				}
				return
			}
			if gotErr != nil {
				t.Fatalf("unexpected err: %v", gotErr)
			}
			if gotID.PrincipalUserID != tc.wantPrincipal {
				t.Fatalf("principal = %v, want %v", gotID.PrincipalUserID, tc.wantPrincipal)
			}
			if gotID.InitiatorUserID != tc.wantInitiator {
				t.Fatalf("initiator = %v, want %v", gotID.InitiatorUserID, tc.wantInitiator)
			}
		})
	}
}
