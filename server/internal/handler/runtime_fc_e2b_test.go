package handler

import (
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
)

func TestFCE2BTemplateCapabilities(t *testing.T) {
	cases := []struct {
		template service.FCE2BTemplate
		want     []string
	}{
		{service.FCE2BTemplate{Template: "multica-fc-hermes-v1"}, []string{"hermes"}},
		{service.FCE2BTemplate{Template: "multica-fc-hermes-dws-v1"}, []string{"hermes", "dws"}},
		{service.FCE2BTemplate{Template: "multica-fc-opencode-v1"}, []string{"opencode"}},
		{service.FCE2BTemplate{Template: "multica-fc-opencode-dws-v1"}, []string{"opencode", "dws"}},
		{service.FCE2BTemplate{Template: "custom-team-template", Name: "OpenCode Team"}, []string{"opencode"}},
	}
	for _, tc := range cases {
		t.Run(tc.template.Template, func(t *testing.T) {
			provider := service.FCE2BProviderForTemplate(tc.template.Template, tc.template.ID, tc.template.Name)
			if got := fcE2BTemplateCapabilities(provider, tc.template); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("fcE2BTemplateCapabilities(%q) = %#v, want %#v", tc.template.Template, got, tc.want)
			}
		})
	}
}

func TestResolveFCE2BProvider(t *testing.T) {
	dual := service.FCE2BTemplate{Template: "multica-fc-team-v1"}
	cases := []struct {
		requested string
		template  service.FCE2BTemplate
		want      string
		ok        bool
	}{
		{"", dual, "hermes", true},
		{"", service.FCE2BTemplate{Template: "multica-fc-opencode-v1"}, "opencode", true},
		{"hermes", dual, "hermes", true},
		{"opencode", dual, "opencode", true},
		{" OpenCode ", dual, "opencode", true},
		// An explicit provider wins over the template-name default.
		{"hermes", service.FCE2BTemplate{Template: "multica-fc-opencode-v1"}, "hermes", true},
		{"codex", dual, "", false},
		{"claude", dual, "", false},
	}
	for _, tc := range cases {
		got, ok := resolveFCE2BProvider(tc.requested, tc.template)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("resolveFCE2BProvider(%q, %q) = (%q, %v), want (%q, %v)",
				tc.requested, tc.template.Template, got, ok, tc.want, tc.ok)
		}
	}
}

func TestDefaultFCE2BRuntimeName(t *testing.T) {
	cases := []struct {
		provider string
		template service.FCE2BTemplate
		want     string
	}{
		{"hermes", service.FCE2BTemplate{Template: "multica-fc-hermes-v1"}, "FC-Hermes-V1"},
		{"opencode", service.FCE2BTemplate{Template: "multica-fc-opencode-v1"}, "FC-Opencode-V1"},
		// Dual-CLI template: the provider is prefixed so the two default
		// names cannot collide.
		{"hermes", service.FCE2BTemplate{Template: "multica-fc-team-v1"}, "FC-Hermes-Team-V1"},
		{"opencode", service.FCE2BTemplate{Template: "multica-fc-team-v1"}, "FC-Opencode-Team-V1"},
		{"hermes", service.FCE2BTemplate{}, "FC-Hermes"},
		{"opencode", service.FCE2BTemplate{}, "FC-Opencode"},
	}
	for _, tc := range cases {
		if got := defaultFCE2BRuntimeName(tc.provider, tc.template); got != tc.want {
			t.Fatalf("defaultFCE2BRuntimeName(%q, %q) = %q, want %q",
				tc.provider, tc.template.Template, got, tc.want)
		}
	}
}

func TestRuntimeSlug(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"FC-Hermes", "fc-hermes"},
		{"  FC  Hermes!!!  ", "fc-hermes"},
		{"中文 Hermes", "hermes"},
		{"中文", "fc-hermes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimeSlug(tc.name); got != tc.want {
				t.Fatalf("runtimeSlug(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}
