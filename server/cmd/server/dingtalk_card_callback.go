package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	dingTalkCardCallbackPath             = "/api/dingtalk/card/customer-feedback"
	dingTalkCardSignatureTimestampHeader = "x-ddpaas-signature-timestamp"
	dingTalkCardSignatureHeader          = "x-ddpaas-signature"
	dingTalkCardAITableWebhookURLEnv      = "AITABLE_WEBHOOK_URL"
	dingTalkCardCallbackSecretEnv         = "DINGTALK_CARD_CALLBACK_SECRET"
	dingTalkCardAITableResponseLimit      = 64 * 1024
	dingTalkCardAITableTimeout            = 2 * time.Second
)

type dingTalkCardCallbackConfig struct {
	AITableWebhookURL string
	CallbackSecret    string
}

func dingTalkCardCallbackConfigFromEnv() dingTalkCardCallbackConfig {
	return dingTalkCardCallbackConfig{
		AITableWebhookURL: strings.TrimSpace(os.Getenv(dingTalkCardAITableWebhookURLEnv)),
		CallbackSecret:    os.Getenv(dingTalkCardCallbackSecretEnv),
	}
}

func computeDingTalkCardCallbackSignature(secret, timestamp string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func verifyDingTalkCardCallbackSignature(request *http.Request, secret string) bool {
	timestamp := request.Header.Get(dingTalkCardSignatureTimestampHeader)
	received := request.Header.Get(dingTalkCardSignatureHeader)
	if secret == "" || timestamp == "" || received == "" {
		return false
	}
	expected := computeDingTalkCardCallbackSignature(secret, timestamp)
	return hmac.Equal([]byte(received), []byte(expected))
}

func dingTalkCardCallbackHandler(config dingTalkCardCallbackConfig, client *http.Client) http.HandlerFunc {
	if client == nil {
		client = &http.Client{Timeout: dingTalkCardAITableTimeout}
	}
	return func(writer http.ResponseWriter, request *http.Request) {
		if config.AITableWebhookURL == "" || config.CallbackSecret == "" {
			writeDingTalkCardCallbackError(writer, http.StatusServiceUnavailable, "service_not_configured")
			return
		}
		if !verifyDingTalkCardCallbackSignature(request, config.CallbackSecret) {
			writeDingTalkCardCallbackError(writer, http.StatusUnauthorized, "invalid_signature")
			return
		}

		rawBody, err := io.ReadAll(request.Body)
		if err != nil {
			writeDingTalkCardCallbackError(writer, http.StatusBadRequest, "invalid_request_body")
			return
		}
		downstreamRequest, err := http.NewRequestWithContext(
			request.Context(),
			http.MethodPost,
			config.AITableWebhookURL,
			bytes.NewReader(rawBody),
		)
		if err != nil {
			writeDingTalkCardCallbackError(writer, http.StatusBadGateway, "aitable_unavailable")
			return
		}
		contentType := request.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		downstreamRequest.Header.Set("Content-Type", contentType)

		downstreamResponse, err := client.Do(downstreamRequest)
		if err != nil {
			writeDingTalkCardCallbackError(writer, http.StatusBadGateway, "aitable_unavailable")
			return
		}
		defer downstreamResponse.Body.Close()
		if downstreamResponse.StatusCode < http.StatusOK || downstreamResponse.StatusCode >= http.StatusMultipleChoices {
			writeDingTalkCardCallbackError(writer, http.StatusBadGateway, "aitable_rejected")
			return
		}

		var downstreamBody struct {
			Success bool `json:"success"`
		}
		if err := json.NewDecoder(io.LimitReader(downstreamResponse.Body, dingTalkCardAITableResponseLimit)).Decode(&downstreamBody); err != nil || !downstreamBody.Success {
			writeDingTalkCardCallbackError(writer, http.StatusBadGateway, "aitable_rejected")
			return
		}

		writeDingTalkCardCallbackJSON(writer, http.StatusOK, map[string]any{
			"cardUpdateOptions": map[string]bool{
				"updateCardDataByKey":    true,
				"updatePrivateDataByKey": false,
			},
			"cardData": map[string]any{
				"cardParamMap": map[string]string{
					"formState":        "disabled",
					"formDisabled":     "true",
					"submitButtonText": "已提交",
				},
			},
		})
	}
}

func writeDingTalkCardCallbackError(writer http.ResponseWriter, status int, code string) {
	writeDingTalkCardCallbackJSON(writer, status, map[string]string{"error": code})
}

func writeDingTalkCardCallbackJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}
