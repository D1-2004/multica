package contextcap

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

func testBox(t *testing.T) *secretbox.Box {
	t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte("c"), secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestSealCredentialBindsEveryScopeField(t *testing.T) {
	box := testBox(t)
	binding := CredentialBinding{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		AgentID:     "22222222-2222-4222-8222-222222222222",
		ConnectorID: "33333333-3333-4333-8333-333333333333",
		ScopeType:   ScopePerson,
		OrgID:       "org-1",
		ScopeKey:    "staff-1",
	}
	sealed, err := SealCredential(box, binding, "person-secret")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("person-secret")) {
		t.Fatal("ciphertext contains the plaintext bearer")
	}
	upper := binding
	upper.AgentID = strings.ToUpper(binding.AgentID)
	if bearer, err := OpenCredential(box, upper, sealed); err != nil || bearer != "person-secret" {
		t.Fatalf("matching binding rejected: %q %v", bearer, err)
	}

	mutations := map[string]func(*CredentialBinding){
		"workspace": func(b *CredentialBinding) { b.WorkspaceID = "44444444-4444-4444-8444-444444444444" },
		"agent":     func(b *CredentialBinding) { b.AgentID = "44444444-4444-4444-8444-444444444444" },
		"connector": func(b *CredentialBinding) { b.ConnectorID = "44444444-4444-4444-8444-444444444444" },
		"scope":     func(b *CredentialBinding) { b.ScopeType = ScopeScene },
		"org":       func(b *CredentialBinding) { b.OrgID = "org-2" },
		"key":       func(b *CredentialBinding) { b.ScopeKey = "staff-2" },
	}
	for name, mutate := range mutations {
		other := binding
		mutate(&other)
		if _, err := OpenCredential(box, other, sealed); !errors.Is(err, ErrCredentialUnavailable) {
			t.Errorf("%s mismatch accepted: %v", name, err)
		}
	}
	if _, err := OpenCredential(nil, binding, sealed); !errors.Is(err, ErrCredentialKeyUnavailable) {
		t.Fatalf("missing key: %v", err)
	}
	if _, err := OpenCredential(box, binding, append([]byte(nil), sealed[:len(sealed)-1]...)); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("tampered ciphertext: %v", err)
	}
}

func TestOpenCredentialRejectsUnsafeSealedBearer(t *testing.T) {
	box := testBox(t)
	binding := CredentialBinding{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		AgentID:     "22222222-2222-4222-8222-222222222222",
		ConnectorID: "33333333-3333-4333-8333-333333333333",
		ScopeType:   ScopeScene,
		ScopeKey:    "aaaaaaaa-0000-4000-8000-000000000003",
	}
	for _, bearer := range []string{"", "a\r\nInjected: 1", "a\x00b"} {
		payload := `{"workspace_id":"11111111-1111-4111-8111-111111111111","agent_id":"22222222-2222-4222-8222-222222222222",` +
			`"connector_id":"33333333-3333-4333-8333-333333333333","scope_type":"scene","org_id":"","scope_key":"aaaaaaaa-0000-4000-8000-000000000003","bearer":` +
			jsonString(bearer) + `}`
		sealed, err := box.Seal([]byte(payload))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := OpenCredential(box, binding, sealed); !errors.Is(err, ErrCredentialUnavailable) {
			t.Errorf("unsafe bearer %q opened: %v", bearer, err)
		}
	}
}

func TestSealCredentialValidatesInput(t *testing.T) {
	box := testBox(t)
	binding := CredentialBinding{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		AgentID:     "22222222-2222-4222-8222-222222222222",
		ConnectorID: "33333333-3333-4333-8333-333333333333",
		ScopeType:   ScopeScene,
		ScopeKey:    "aaaaaaaa-0000-4000-8000-000000000003",
	}
	for _, bearer := range []string{"", " padded", "line\nbreak", "nul\x00", strings.Repeat("x", MaxBearerLength+1)} {
		if _, err := SealCredential(box, binding, bearer); !errors.Is(err, ErrInvalidBearer) {
			t.Errorf("bearer %q accepted: %v", bearer, err)
		}
	}
	if _, err := SealCredential(box, binding, strings.Repeat("x", MaxBearerLength)); err != nil {
		t.Fatalf("max-length bearer rejected: %v", err)
	}
	offer := binding
	offer.ScopeType = ScopeOffer
	if _, err := SealCredential(box, offer, "token"); !errors.Is(err, ErrInvalidCredentialBinding) {
		t.Fatalf("offer scope accepted: %v", err)
	}
	badID := binding
	badID.ConnectorID = "not-a-uuid"
	if _, err := SealCredential(box, badID, "token"); !errors.Is(err, ErrInvalidCredentialBinding) {
		t.Fatalf("invalid connector id accepted: %v", err)
	}
	if _, err := SealCredential(nil, binding, "token"); !errors.Is(err, ErrCredentialKeyUnavailable) {
		t.Fatalf("nil box: %v", err)
	}
}

func TestHint(t *testing.T) {
	for _, tc := range []struct{ bearer, want string }{
		{"", "••••"},
		{"short", "••••"},
		{"1234567", "••••"},
		{"12345678", "••••5678"},
		{"令牌令牌令牌令牌尾巴", "••••令牌尾巴"},
	} {
		if got := Hint(tc.bearer); got != tc.want {
			t.Errorf("Hint(%q) = %q, want %q", tc.bearer, got, tc.want)
		}
	}
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\r':
			b.WriteString(`\r`)
		case '\n':
			b.WriteString(`\n`)
		case 0:
			b.WriteString(`\u0000`)
		case '"':
			b.WriteString(`\"`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
