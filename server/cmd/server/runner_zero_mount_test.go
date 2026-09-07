package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/runnerws"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRunnerWebSocketAcceptsOwnedMachineWithoutAgentMounts(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	conn, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	defer conn.Close(context.Background())

	ctx := context.Background()
	schemaName := "runner_ws_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	schemaSQL := pgx.Identifier{schemaName}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schemaSQL); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA "+schemaSQL+" CASCADE")
	})
	if _, err := conn.Exec(ctx, "SET search_path TO "+schemaSQL); err != nil {
		t.Fatalf("set search path: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		CREATE TABLE runner_machine (
			id uuid PRIMARY KEY, owner_id uuid NOT NULL, name text NOT NULL, os text NOT NULL,
			arch text NOT NULL, public_key bytea NOT NULL, client_version text NOT NULL,
			last_seen_at timestamptz, revoked_at timestamptz, revoked_by uuid,
			created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
			connection_id uuid, connected_at timestamptz
		);
		CREATE TABLE agent_runner_binding (
			id uuid PRIMARY KEY, machine_id uuid NOT NULL, revoked_at timestamptz,
			disconnected_at timestamptz
		);
		CREATE TABLE runner_auth_challenge (
			id uuid PRIMARY KEY, machine_id uuid NOT NULL, challenge_hash text NOT NULL,
			expires_at timestamptz NOT NULL, consumed_at timestamptz,
			created_at timestamptz NOT NULL DEFAULT now()
		);
	`); err != nil {
		t.Fatalf("create Runner tables: %v", err)
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	machineID := uuid.New()
	challengeID := uuid.New()
	challenge := "zero-mount-challenge"
	if _, err := conn.Exec(ctx, `
		INSERT INTO runner_machine (id, owner_id, name, os, arch, public_key, client_version)
		VALUES ($1, $2, 'Zero mount machine', 'darwin', 'arm64', $3, 'test')
	`, machineID, uuid.New(), publicKey); err != nil {
		t.Fatalf("seed Runner machine: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO runner_auth_challenge (id, machine_id, challenge_hash, expires_at)
		VALUES ($1, $2, $3, now() + interval '1 minute')
	`, challengeID, machineID, auth.HashToken(challenge)); err != nil {
		t.Fatalf("seed Runner authentication: %v", err)
	}

	hub := runnerws.NewHub()
	h := &handler.Handler{Queries: db.New(conn), RunnerHub: hub}
	server := httptest.NewServer(http.HandlerFunc(h.RunnerWebSocket))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/runner/ws?machine_id=" + machineID.String()
	headers := http.Header{}
	headers.Set("X-Runner-Challenge-ID", challengeID.String())
	headers.Set("X-Runner-Challenge", challenge)
	headers.Set("X-Runner-Signature", base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(challenge))))
	connWS, response, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		status := 0
		body := ""
		if response != nil {
			status = response.StatusCode
			if raw, readErr := io.ReadAll(response.Body); readErr == nil {
				body = string(raw)
			}
		}
		t.Fatalf("connect zero-mount Runner WebSocket: %v (status=%d body=%q)", err, status, body)
	}
	defer connWS.Close()
	deadline := time.Now().Add(time.Second)
	registered := false
	for time.Now().Before(deadline) {
		if hub.Connected(machineID.String()) {
			registered = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !registered {
		t.Fatal("zero-mount Runner WebSocket was not registered")
	}
	h.DeliverRunnerMachine(machineID.String(), []byte(`{"type":"runner:bindings_changed","active_binding_count":0}`), "")
	time.Sleep(100 * time.Millisecond)
	if !hub.Connected(machineID.String()) {
		t.Fatal("zero-mount notification closed the Runner WebSocket")
	}
}
