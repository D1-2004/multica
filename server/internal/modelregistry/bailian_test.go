package modelregistry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type bailianTransport func(*http.Request) (*http.Response, error)

func (f bailianTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBailianEndpointDetection(t *testing.T) {
	for _, v := range []struct {
		base string
		want bool
	}{
		{"https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", true},
		{"https://dashscope.aliyuncs.com/compatible-mode/v1/", true},
		{"https://dashscope.aliyuncs.com.evil.test/compatible-mode/v1", false},
		{"https://example.com/compatible-mode/v1", false},
		{"https://u:secret@dashscope.aliyuncs.com/compatible-mode/v1", false},
		{"https://dashscope.aliyuncs.com/other", false},
	} {
		if got := BailianAPIOrigin(v.base) != ""; got != v.want {
			t.Fatalf("endpoint %s: %v", v.base, got)
		}
	}
}
func TestBailianDiscoveryRequiresCapabilitiesAndInferenceAccess(t *testing.T) {
	p := Provider{BaseURL: "https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", APIKey: "test-key"}
	client := &http.Client{Transport: bailianTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "workspace.cn-beijing.maas.aliyuncs.com" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("wrong credential destination")
		}
		code := 200
		body := ""
		if r.Method == "GET" {
			if r.URL.Query().Get("features") != "function-calling" {
				t.Error("missing capability filter")
			}
			if r.URL.Query().Get("page_no") == "1" {
				body = `{"success":true,"output":{"total":6,"models":[{"model":"qwen","features":["function-calling"],"capabilities":["TG"]},{"model":"third-party","features":["function-calling"],"capabilities":["TG"]},{"model":"image","features":["function-calling"],"capabilities":["IG"]}]}}`
			} else {
				body = `{"success":true,"output":{"total":6,"models":[{"model":"blocked","features":["function-calling"],"capabilities":["TG"]},{"model":"limited","features":["function-calling"],"capabilities":["TG"]},{"model":"not-compatible","features":["function-calling"],"capabilities":["TG"]}]}}`
			}
		} else {
			var input struct {
				Model     string `json:"model"`
				MaxTokens int    `json:"max_tokens"`
			}
			json.NewDecoder(r.Body).Decode(&input)
			if input.MaxTokens != 1 {
				t.Error("unbounded inference probe")
			}
			switch input.Model {
			case "qwen", "third-party":
				body = `{"choices":[{"message":{"content":"OK"}}]}`
			case "blocked":
				code = 400
				body = `{"error":{"message":"product not activated"}}`
			case "limited":
				code = 429
				body = `{}`
			default:
				t.Errorf("ineligible model probed: %s", input.Model)
			}
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	got, err := DiscoverBailian(context.Background(), p, []string{"qwen", "third-party", "image", "blocked", "limited"}, client)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Models, []string{"qwen", "third-party"}) || !got.Replace || got.Checked != 4 || got.Unavailable != 1 || got.Unverified != 1 {
		t.Fatalf("unexpected discovery: %+v", got)
	}
}
func TestBailianCatalogFailureDoesNotFallBackToUnfilteredModels(t *testing.T) {
	client := &http.Client{Transport: bailianTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	_, err := DiscoverBailian(context.Background(), Provider{BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"}, []string{"blocked"}, client)
	if err == nil {
		t.Fatal("expected failed discovery")
	}
}
