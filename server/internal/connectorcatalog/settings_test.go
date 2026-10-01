package connectorcatalog

import "testing"

func TestSettingsCatalogMatchesTheProductList(t *testing.T) {
	want := []struct {
		slug string
		mode string
	}{
		{"github", "preregistered"},
		{"slack", "preregistered"},
		{"asana", "preregistered"},
		{"google", "preregistered"},
		{"dropbox", "preregistered"},
		{"box", "preregistered"},
		{"notion", "dcr"},
		{"linear", "dcr"},
		{"sentry", "dcr"},
		{"atlassian", "dcr"},
		{"figma", "limited"},
		{"stripe", "dcr"},
	}
	got := SettingsCatalog()
	if len(got) != len(want) {
		t.Fatalf("catalog = %d, want %d", len(got), len(want))
	}
	for i, spec := range got {
		if spec.Slug != want[i].slug || spec.Mode != want[i].mode || spec.Name == "" {
			t.Fatalf("row %d = %+v", i, spec)
		}
		if spec.Mode == "preregistered" {
			if spec.AuthorizationEndpoint == "" || spec.TokenEndpoint == "" || len(spec.Fields) == 0 || spec.DocsURL == "" {
				t.Fatalf("%s is missing a preset or a field", spec.Slug)
			}
		} else if len(spec.Fields) != 0 || spec.OAuthConnect {
			t.Fatalf("%s should not ask for fields", spec.Slug)
		}
	}
	asana, ok := SettingsSpecFor("asana")
	if !ok || asana.KnownClientID != "1219049145290369" || !asana.OAuthConnect {
		t.Fatalf("asana settings = %+v", asana)
	}
	if ProductionCallbackURL != "https://fde-workbench.dingtalk.com/api/connectors/oauth/callback" {
		t.Fatal(ProductionCallbackURL)
	}
}
