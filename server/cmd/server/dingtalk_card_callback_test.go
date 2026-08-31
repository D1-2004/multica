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

	"github.com/go-chi/chi/v5"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func dingTalkCardCallbackRequest(rawBody string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/dingtalk/card/customer-feedback/test-flow", strings.NewReader(rawBody))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	return request
}

func dingTalkCardCallbackRouter(client *http.Client) http.Handler {
	router := chi.NewRouter()
	router.Post(dingTalkCardCallbackPath, dingTalkCardCallbackHandler(client))
	return router
}

func dingTalkCardCallbackClientForServer(server *httptest.Server) *http.Client {
	transport := server.Client().Transport
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		forwardedRequest := request.Clone(request.Context())
		forwardedURL := *request.URL
		forwardedURL.Scheme = "http"
		forwardedURL.Host = strings.TrimPrefix(server.URL, "http://")
		forwardedRequest.URL = &forwardedURL
		return transport.RoundTrip(forwardedRequest)
	})}
}

func TestDingTalkCardCallbackRoutesFlowAndForwardsRawBody(t *testing.T) {
	const flowID = "103b082bde2f2107d5c80007"
	rawBody := "{\n  \"type\": \"actionCallback\",\n  \"unknown\": [1, 2, 3]\n}\n"
	var receivedURL string
	var receivedBody []byte
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var err error
		receivedURL = request.URL.String()
		receivedBody, err = io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read downstream body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"success":true}`)),
		}, nil
	})}

	router := dingTalkCardCallbackRouter(client)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/dingtalk/card/customer-feedback/"+flowID, strings.NewReader(rawBody))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if receivedURL != "https://connector.dingtalk.com/webhook/flow/"+flowID {
		t.Fatalf("downstream URL = %q", receivedURL)
	}
	if !bytes.Equal(receivedBody, []byte(rawBody)) {
		t.Fatalf("forwarded body changed:\n got: %q\nwant: %q", receivedBody, rawBody)
	}
}

func TestDingTalkCardCallbackRejectsMissingOrInvalidFlowIDWithoutDownstreamRequest(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "missing", path: "/api/dingtalk/card/customer-feedback"},
		{name: "dot", path: "/api/dingtalk/card/customer-feedback/abc.def"},
		{name: "encoded dot", path: "/api/dingtalk/card/customer-feedback/abc%2Edef"},
		{name: "slash", path: "/api/dingtalk/card/customer-feedback/abc/def"},
		{name: "encoded slash", path: "/api/dingtalk/card/customer-feedback/abc%2Fdef"},
		{name: "too long", path: "/api/dingtalk/card/customer-feedback/" + strings.Repeat("a", 129)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"success":true}`)),
				}, nil
			})}
			router := dingTalkCardCallbackRouter(client)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{"arbitrary":true}`)))

			if recorder.Code < http.StatusBadRequest {
				t.Fatalf("status = %d, want rejection", recorder.Code)
			}
			if calls.Load() != 0 {
				t.Fatalf("downstream calls = %d, want 0", calls.Load())
			}
		})
	}
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

	handler := dingTalkCardCallbackRouter(dingTalkCardCallbackClientForServer(downstream))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, dingTalkCardCallbackRequest(rawBody))

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

func TestDingTalkCardCallbackDoesNotRequireAuthentication(t *testing.T) {
	var calls atomic.Int32
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	defer downstream.Close()

	handler := dingTalkCardCallbackRouter(dingTalkCardCallbackClientForServer(downstream))
	request := dingTalkCardCallbackRequest(`{"arbitrary":true}`)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if calls.Load() != 1 {
		t.Fatalf("downstream calls = %d, want 1", calls.Load())
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

			handler := dingTalkCardCallbackRouter(dingTalkCardCallbackClientForServer(downstream))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, dingTalkCardCallbackRequest(`{"arbitrary":true}`))

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
	client := dingTalkCardCallbackClientForServer(downstream)
	downstream.Close()

	handler := dingTalkCardCallbackRouter(client)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, dingTalkCardCallbackRequest(`{"arbitrary":true}`))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if strings.Contains(recorder.Body.String(), "cardData") {
		t.Fatalf("failure response marks card submitted: %s", recorder.Body.String())
	}
}

func TestDingTalkCardCallbackDoesNotUseFixedRuntimeConfig(t *testing.T) {
	for _, path := range []string{"../../../src/main.sh", "../../../.env.example"} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(source), "AITABLE_WEBHOOK_URL") {
			t.Errorf("%s still references AITABLE_WEBHOOK_URL", path)
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
