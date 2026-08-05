package middleware

import (
	"context"
	"testing"
)

func TestWorkspaceAccessPrincipalContextCarriesLifecycleIdentity(t *testing.T) {
	want := WorkspaceAccessPrincipal{
		TokenID:     "11111111-1111-4111-8111-111111111111",
		UserID:      "22222222-2222-4222-8222-222222222222",
		WorkspaceID: "33333333-3333-4333-8333-333333333333",
		Name:        "Vendor",
		Version:     2,
	}
	got, ok := WorkspaceAccessPrincipalFromContext(WithWorkspaceAccessPrincipal(context.Background(), want))
	if !ok || got != want {
		t.Fatalf("principal = %+v, ok=%v, want %+v", got, ok, want)
	}
}
