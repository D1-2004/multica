package main

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
)

// Build the production router, not a handler fixture that installs the callback.
func TestRouterRegistersEmployeeHumanReceiptAfterResponseServiceConstruction(t *testing.T) {
	t.Setenv("AGENT_MESSAGE_ROUTER_INTERNAL_URL", "http://127.0.0.1:1")
	t.Setenv("AGENT_MESSAGE_ROUTER_SERVICE_CREDENTIAL", "test-only-router-credential")
	_, h := NewRouterWithOptions(testPool, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil, RouterOptions{})
	if h.DingTalkResponses == nil {
		t.Fatal("managed response service was not constructed")
	}
	if h.DingTalkResponses.OnA2UIAccepted == nil {
		t.Fatal("native card receipt callback was not registered on the constructed service")
	}
}
