package agentmessagerouter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

var llmTraceCallbackPattern = regexp.MustCompile(`^/api/v1/dispatch-tasks/[A-Za-z0-9_-]{1,128}/llm-traces$`)

func (c *Client) SubmitLLMTrace(
	ctx context.Context,
	callbackPath string,
	capability string,
	payload []byte,
) (int, error) {
	if !llmTraceCallbackPattern.MatchString(callbackPath) {
		return 0, errors.New("agent message router LLM trace callback path is invalid")
	}
	if !validLLMTraceCapability(capability) {
		return 0, errors.New("agent message router LLM trace capability is invalid")
	}
	response, err := c.doWithBearer(
		ctx,
		http.MethodPost,
		callbackPath,
		capability,
		bytes.NewReader(payload),
	)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxRouterResponseBytes))
	return response.StatusCode, nil
}

func validLLMTraceCapability(capability string) bool {
	if capability == "" || len(capability) > 4096 || strings.TrimSpace(capability) != capability {
		return false
	}
	for _, value := range capability {
		if unicode.IsControl(value) || unicode.IsSpace(value) {
			return false
		}
	}
	return true
}
