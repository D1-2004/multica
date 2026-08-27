package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

const testDingTalkCardCallbackSecret = "test-callback-secret"

func signedDingTalkCardCallbackRequest(t *testing.T, rawBody string, secret string) *http.Request {
	t.Helper()
	timestamp := "1787875200123"
	request := httptest.NewRequest(http.MethodPost, dingTalkCardCallbackPath, strings.NewReader(rawBody))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set(dingTalkCardSignatureTimestampHeader, timestamp)
	request.Header.Set(dingTalkCardSignatureHeader, computeDingTalkCardCallbackSignature(secret, timestamp))
	return request
}

func TestDingTalkCardCallbackForwardsRawBodyAndReturnsCardUpdate(t *testing.T) {
	rawBody := "{\n  \"type\": \"actionCallback\",\n  \"content\": \"{\\\"cardPrivateData\\\":{\\\"params\\\":{\\\"unexpected\\\":true}}}\",\n  \"unknown\": [1, 2, 3]\n}\n"
	receivedBody := make(chan []byte, 1)
	receivedContentType := make(chan string, 1)
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read downstream body: %v", err)
		}
		receivedBody <- body
		receivedContentType <- r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true,"data":true}`)
	}))
	defer downstream.Close()

	handler := dingTalkCardCallbackHandler(dingTalkCardCallbackConfig{
		AITableWebhookURL: downstream.URL,
		CallbackSecret:    testDingTalkCardCallbackSecret,
	}, downstream.Client())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedDingTalkCardCallbackRequest(t, rawBody, testDingTalkCardCallbackSecret))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := <-receivedBody; !bytes.Equal(got, []byte(rawBody)) {
		t.Fatalf("forwarded body changed:\n got: %q\nwant: %q", got, rawBody)
	}
	if got := <-receivedContentType; got != "application/json; charset=utf-8" {
		t.Fatalf("forwarded content type = %q", got)
	}

	var response struct {
		CardUpdateOptions struct {
			UpdateCardDataByKey    bool `json:"updateCardDataByKey"`
			UpdatePrivateDataByKey bool `json:"updatePrivateDataByKey"`
		} `json:"cardUpdateOptions"`
		CardData struct {
			CardParamMap map[string]string `json:"cardParamMap"`
		} `json:"cardData"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.CardUpdateOptions.UpdateCardDataByKey || response.CardUpdateOptions.UpdatePrivateDataByKey {
		t.Fatalf("unexpected card update options: %#v", response.CardUpdateOptions)
	}
	wantParams := map[string]string{
		"formState":       "disabled",
		"formDisabled":    "true",
		"submitButtonText": "已提交",
	}
	if !mapsEqual(response.CardData.CardParamMap, wantParams) {
		t.Fatalf("card params = %#v, want %#v", response.CardData.CardParamMap, wantParams)
	}
}

func TestDingTalkCardCallbackRejectsInvalidSignatureWithoutForwarding(t *testing.T) {
	var calls atomic.Int32
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer downstream.Close()

	handler := dingTalkCardCallbackHandler(dingTalkCardCallbackConfig{
		AITableWebhookURL: downstream.URL,
		CallbackSecret:    testDingTalkCardCallbackSecret,
	}, downstream.Client())
	request := signedDingTalkCardCallbackRequest(t, `{"arbitrary":true}`, "wrong-secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if calls.Load() != 0 {
		t.Fatalf("downstream calls = %d, want 0", calls.Load())
	}
	if strings.Contains(recorder.Body.String(), "cardData") {
		t.Fatalf("failure response marks card submitted: %s", recorder.Body.String())
	}
}

func TestDingTalkCardCallbackDoesNotUpdateCardWhenWebhookRejects(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "http error", statusCode: http.StatusInternalServerError, body: `{"success":true}`},
		{name: "business failure", statusCode: http.StatusOK, body: `{"success":false}`},
		{name: "missing success", statusCode: http.StatusOK, body: `{"data":true}`},
		{name: "invalid response", statusCode: http.StatusOK, body: `not-json`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.statusCode)
				_, _ = io.WriteString(w, test.body)
			}))
			defer downstream.Close()

			handler := dingTalkCardCallbackHandler(dingTalkCardCallbackConfig{
				AITableWebhookURL: downstream.URL,
				CallbackSecret:    testDingTalkCardCallbackSecret,
			}, downstream.Client())
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, signedDingTalkCardCallbackRequest(t, `{"arbitrary":true}`, testDingTalkCardCallbackSecret))

			if recorder.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
			}
			if strings.Contains(recorder.Body.String(), "cardData") {
				t.Fatalf("failure response marks card submitted: %s", recorder.Body.String())
			}
		})
	}
}

func TestDingTalkCardCallbackDoesNotUpdateCardOnNetworkFailure(t *testing.T) {
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	client := downstream.Client()
	downstreamURL := downstream.URL
	downstream.Close()

	handler := dingTalkCardCallbackHandler(dingTalkCardCallbackConfig{
		AITableWebhookURL: downstreamURL,
		CallbackSecret:    testDingTalkCardCallbackSecret,
	}, client)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedDingTalkCardCallbackRequest(t, `{"arbitrary":true}`, testDingTalkCardCallbackSecret))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if strings.Contains(recorder.Body.String(), "cardData") {
		t.Fatalf("failure response marks card submitted: %s", recorder.Body.String())
	}
}

func TestDingTalkCardCallbackRuntimeConfigKeysAreWhitelisted(t *testing.T) {
	source, err := os.ReadFile("../../../src/main.sh")
	if err != nil {
		t.Fatalf("read src/main.sh: %v", err)
	}
	for _, key := range []string{dingTalkCardAITableWebhookURLEnv, dingTalkCardCallbackSecretEnv} {
		if !strings.Contains(string(source), "  "+key+"\n") {
			t.Errorf("runtime config key %s is not whitelisted", key)
		}
	}
}

func mapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
