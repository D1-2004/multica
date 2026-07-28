package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/sandboxrelay"
	"github.com/multica-ai/multica/server/internal/service"
)

func TestNewRouterWithOptionsWiresSandboxRelaySignerToRequestLauncher(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate relay signing key: %v", err)
	}
	signer, err := sandboxrelay.NewSigner("prepub-test", privateKey)
	if err != nil {
		t.Fatalf("create relay signer: %v", err)
	}

	_, h := NewRouterWithOptions(
		testPool,
		realtime.NewHub(),
		events.New(),
		analytics.NoopClient{},
		nil,
		RouterOptions{SandboxRelaySigner: signer},
	)

	if h.FCE2BLauncher.SandboxRelaySigner != signer {
		t.Fatal("request-path FC/E2B launcher did not receive the sandbox relay signer")
	}
	requestLauncher, ok := h.TaskService.RuntimeLauncher.(*service.FCE2BLauncher)
	if !ok {
		t.Fatalf("request-path runtime launcher has type %T, want *service.FCE2BLauncher", h.TaskService.RuntimeLauncher)
	}
	if requestLauncher != h.FCE2BLauncher {
		t.Fatal("request-path task service and handler use different FC/E2B launchers")
	}
}
