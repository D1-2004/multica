package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	dingTalkCardCallbackPath        = "/api/dingtalk/card/customer-feedback/{flowId}"
	dingTalkCardAITableWebhookBaseURL = "https://connector.dingtalk.com/webhook/flow/"
	dingTalkCardFlowIDMaxLength     = 128
	dingTalkCardAITableResponseLimit = 64 * 1024
	dingTalkCardAITableTimeout       = 2 * time.Second
)

func dingTalkCardCallbackHandler(client *http.Client) http.HandlerFunc {
	if client == nil {
		client = &http.Client{Timeout: dingTalkCardAITableTimeout}
	}
	return func(writer http.ResponseWriter, request *http.Request) {
		flowID := chi.URLParam(request, "flowId")
		if !validDingTalkCardFlowID(flowID) {
			writeDingTalkCardCallbackError(writer, http.StatusBadRequest, "invalid_flow_id")
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
			dingTalkCardAITableWebhookBaseURL+flowID,
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

func validDingTalkCardFlowID(flowID string) bool {
	if len(flowID) == 0 || len(flowID) > dingTalkCardFlowIDMaxLength {
		return false
	}
	for index := 0; index < len(flowID); index++ {
		character := flowID[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
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
