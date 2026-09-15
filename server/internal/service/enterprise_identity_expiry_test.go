package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"testing"
	"time"
)

func expiryTestBUCID(expires time.Time) string {
	return "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, expires.Unix()))) + ".dGVzdA"
}
func TestEnterpriseIdentityRefreshesIDIndependentlyOfAccessToken(t *testing.T) {
	now := time.Date(2026, 9, 15, 7, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, stored string
		fresh        bool
	}{
		{"expired", expiryTestBUCID(now.Add(-time.Second)), false},
		{"near expiry", expiryTestBUCID(now.Add(30 * time.Second)), false},
		{"malformed", "malformed", false},
		{"missing expiry", "eyJhbGciOiJIUzI1NiJ9.e30.dGVzdA", false},
		{"fresh", expiryTestBUCID(now.Add(time.Hour)), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			box, _ := secretbox.New(bytes.Repeat([]byte{0x42}, secretbox.KeySize))
			a, r, i := mustSealBUCIdentityTokens(t, box, BUCIdentityTokens{AccessToken: "access", RefreshToken: "refresh", IDToken: test.stored})
			row := db.AgentEnterpriseIdentity{ID: util.MustParseUUID("44444444-4444-4444-4444-444444444444"), RawEmpID: "12345", Status: "active", BucAccessTokenEncrypted: a, BucRefreshTokenEncrypted: r, BucIDTokenEncrypted: i, BucAccessExpiresAt: pgtype.Timestamptz{Time: now.Add(72 * time.Hour), Valid: true}, TokenVersion: 1}
			store := &fakeEnterpriseIdentityStore{current: row}
			next := expiryTestBUCID(now.Add(2 * time.Hour))
			buc := &fakeBUCOAuthClient{refreshResult: BUCIdentityTokens{AccessToken: "new-access", RefreshToken: "new-refresh", IDToken: next, ExpiresIn: 259200}}
			svc := newTestEnterpriseIdentityService(t, store, buc, &fakeEnterpriseAuthX{}, &fakeEnterpriseIdem{}, &fakeEnterpriseSandboxes{}, now)
			svc.Secrets = box
			_, tokens, err := svc.rotateBUCTokens(context.Background(), row)
			if err != nil {
				t.Fatal(err)
			}
			if test.fresh {
				if buc.refreshFrom != "" || tokens.IDToken != test.stored {
					t.Fatal("refreshed fresh credentials")
				}
				return
			}
			persisted, _ := box.Open(store.current.BucIDTokenEncrypted)
			if buc.refreshFrom != "refresh" || tokens.IDToken != next || string(persisted) != next || store.current.TokenVersion != 2 {
				t.Fatal("new ID token not returned and persisted")
			}
		})
	}
}
func TestEnterpriseIdentityRefreshRejectsMissingIDToken(t *testing.T) {
	now := time.Now()
	box, _ := secretbox.New(bytes.Repeat([]byte{0x42}, secretbox.KeySize))
	a, r, i := mustSealBUCIdentityTokens(t, box, BUCIdentityTokens{AccessToken: "access", RefreshToken: "refresh", IDToken: expiryTestBUCID(now.Add(-time.Hour))})
	row := db.AgentEnterpriseIdentity{Status: "active", BucAccessTokenEncrypted: a, BucRefreshTokenEncrypted: r, BucIDTokenEncrypted: i, TokenVersion: 1}
	store := &fakeEnterpriseIdentityStore{current: row}
	svc := newTestEnterpriseIdentityService(t, store, &fakeBUCOAuthClient{refreshResult: BUCIdentityTokens{AccessToken: "new", RefreshToken: "new", ExpiresIn: 259200}}, &fakeEnterpriseAuthX{}, &fakeEnterpriseIdem{}, &fakeEnterpriseSandboxes{}, now)
	svc.Secrets = box
	if _, _, err := svc.rotateBUCTokensLocked(context.Background(), row); err == nil {
		t.Fatal("reused old ID token")
	}
	if store.current.TokenVersion != 1 {
		t.Fatal("persisted incomplete trio")
	}
}
