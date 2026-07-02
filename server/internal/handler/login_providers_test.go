package handler

import (
	"reflect"
	"testing"
)

func TestLoginProvidersUnrestrictedByDefault(t *testing.T) {
	t.Setenv("LOGIN_PROVIDERS", "")
	t.Setenv("LOGIN_DINGTALK_ONLY", "")

	if got := LoginProviders(); got != nil {
		t.Errorf("LoginProviders() = %v, want nil", got)
	}
	for _, name := range []string{"email", "google", "dingtalk", "lark"} {
		if !LoginProviderAllowed(name) {
			t.Errorf("LoginProviderAllowed(%q) = false, want true when unrestricted", name)
		}
	}
}

func TestLoginProvidersHonorsLegacyDingtalkOnly(t *testing.T) {
	t.Setenv("LOGIN_PROVIDERS", "")
	t.Setenv("LOGIN_DINGTALK_ONLY", "true")

	if got := LoginProviders(); !reflect.DeepEqual(got, []string{"dingtalk"}) {
		t.Errorf("LoginProviders() = %v, want [dingtalk]", got)
	}
	if !LoginProviderAllowed("dingtalk") {
		t.Error("dingtalk should be allowed under the legacy flag")
	}
	for _, name := range []string{"email", "google", "lark"} {
		if LoginProviderAllowed(name) {
			t.Errorf("LoginProviderAllowed(%q) = true, want false under the legacy flag", name)
		}
	}
}

func TestLoginProvidersAllowlistWinsOverLegacyFlag(t *testing.T) {
	t.Setenv("LOGIN_PROVIDERS", "dingtalk,lark")
	t.Setenv("LOGIN_DINGTALK_ONLY", "true")

	if got := LoginProviders(); !reflect.DeepEqual(got, []string{"dingtalk", "lark"}) {
		t.Errorf("LoginProviders() = %v, want [dingtalk lark]", got)
	}
	if !LoginProviderAllowed("lark") {
		t.Error("lark should be allowed when listed, regardless of the legacy flag")
	}
	for _, name := range []string{"email", "google"} {
		if LoginProviderAllowed(name) {
			t.Errorf("LoginProviderAllowed(%q) = true, want false", name)
		}
	}
}

func TestLoginProvidersNormalizesAndDropsUnknown(t *testing.T) {
	t.Setenv("LOGIN_PROVIDERS", " DingTalk , lark , wechat ,, ")
	t.Setenv("LOGIN_DINGTALK_ONLY", "")

	if got := LoginProviders(); !reflect.DeepEqual(got, []string{"dingtalk", "lark"}) {
		t.Errorf("LoginProviders() = %v, want [dingtalk lark]", got)
	}
	if LoginProviderAllowed("wechat") {
		t.Error("unknown provider names must not be allowed")
	}
}
