package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
)

const defaultJWTSecret = "multica-dev-secret-change-in-production"

var (
	jwtSecret     []byte
	jwtSecretOnce sync.Once
)

func JWTSecret() []byte {
	jwtSecretOnce.Do(func() {
		secret := os.Getenv("JWT_SECRET")
		if secret == "" {
			secret = defaultJWTSecret
		}
		jwtSecret = []byte(secret)
	})

	return jwtSecret
}

// GeneratePATToken creates a new personal access token: "mul_" + 40 random hex chars.
func GeneratePATToken() (string, error) {
	b := make([]byte, 20) // 20 bytes = 40 hex chars
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate PAT token: %w", err)
	}
	return "mul_" + hex.EncodeToString(b), nil
}

// GenerateWorkspaceAccessToken creates a workspace-bound DTA credential. The
// prefix is intentionally distinct from personal and machine PATs
// so authentication never falls through to a broader identity type.
func GenerateWorkspaceAccessToken() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate workspace access token: %w", err)
	}
	return "dta_" + hex.EncodeToString(b), nil
}

// GenerateWorkspaceMCPToken creates a credential accepted only by the
// workspace MCP endpoint. It never falls through to PAT or DTA authentication.
func GenerateWorkspaceMCPToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate workspace MCP token: %w", err)
	}
	return "wmcp_" + hex.EncodeToString(b), nil
}

// GenerateA2AToken creates a credential scoped to one published A2A client.
// The distinct prefix prevents this machine credential from ever falling
// through to the broader PAT/JWT authentication paths.
func GenerateA2AToken() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate A2A token: %w", err)
	}
	return "mca2a_" + hex.EncodeToString(b), nil
}

// GenerateDaemonToken creates a new daemon auth token: "mdt_" + 40 random hex chars.
func GenerateDaemonToken() (string, error) {
	b := make([]byte, 20) // 20 bytes = 40 hex chars
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate daemon token: %w", err)
	}
	return "mdt_" + hex.EncodeToString(b), nil
}

// GenerateAgentTaskToken creates a new task-scoped agent auth token:
// "mat_" + 40 random hex chars. The token is single-purpose — bound to a
// specific (agent_id, task_id) pair on the server side — and is what the
// daemon injects into the agent process in place of its own owner PAT.
// See MUL-2600.
func GenerateAgentTaskToken() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate agent task token: %w", err)
	}
	return "mat_" + hex.EncodeToString(b), nil
}

// HashToken returns the hex-encoded SHA-256 hash of a token string.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
