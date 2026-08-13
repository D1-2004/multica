package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type runnerConfig struct {
	ServerURL  string   `json:"server_url"`
	MachineID  string   `json:"machine_id,omitempty"`
	PublicKey  string   `json:"public_key"`
	PrivateKey string   `json:"private_key"`
	Roots      []string `json:"roots"`
}

type runnerConnectionState struct {
	PID         int       `json:"pid"`
	MachineID   string    `json:"machine_id"`
	ConnectedAt time.Time `json:"connected_at"`
}

func runnerStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve Runner state directory: %w", err)
	}
	return filepath.Join(home, ".multica", "runner"), nil
}

func runnerConfigPath() (string, error) {
	dir, err := runnerStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func loadRunnerConfig() (runnerConfig, error) {
	path, err := runnerConfigPath()
	if err != nil {
		return runnerConfig{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return runnerConfig{}, nil
	}
	if err != nil {
		return runnerConfig{}, fmt.Errorf("read Runner config: %w", err)
	}
	var cfg runnerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return runnerConfig{}, fmt.Errorf("parse Runner config: %w", err)
	}
	return cfg, nil
}

func saveRunnerConfig(cfg runnerConfig) error {
	path, err := runnerConfigPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create Runner state directory: %w", err)
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Runner config: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".runner-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create Runner config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write Runner config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close Runner config: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("secure Runner config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("save Runner config: %w", err)
	}
	return nil
}

func ensureRunnerKey(cfg *runnerConfig) error {
	if cfg.PublicKey != "" || cfg.PrivateKey != "" {
		if cfg.PublicKey == "" || cfg.PrivateKey == "" {
			return errors.New("Runner config contains an incomplete key pair")
		}
		if _, err := runnerPrivateKey(*cfg); err != nil {
			return err
		}
		return nil
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate Runner key: %w", err)
	}
	cfg.PublicKey = base64.RawURLEncoding.EncodeToString(publicKey)
	cfg.PrivateKey = base64.RawURLEncoding.EncodeToString(privateKey)
	return nil
}

func runnerPrivateKey(cfg runnerConfig) (ed25519.PrivateKey, error) {
	privateKey, err := base64.RawURLEncoding.DecodeString(cfg.PrivateKey)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("Runner config contains an invalid Ed25519 private key")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(cfg.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize || !bytes.Equal(publicKey, privateKey[ed25519.PrivateKeySize-ed25519.PublicKeySize:]) {
		return nil, errors.New("Runner config contains an invalid Ed25519 key pair")
	}
	return ed25519.PrivateKey(privateKey), nil
}

func runnerPIDPath() (string, error) {
	dir, err := runnerStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runner.pid"), nil
}

func runnerLogPath() (string, error) {
	dir, err := runnerStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runner.log"), nil
}

func runnerConnectionStatePath() (string, error) {
	dir, err := runnerStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "connection.json"), nil
}

func writeRunnerConnectionState(machineID string) error {
	path, err := runnerConnectionStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Runner state directory: %w", err)
	}
	raw, err := json.Marshal(runnerConnectionState{
		PID:         os.Getpid(),
		MachineID:   machineID,
		ConnectedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("encode Runner connection state: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write Runner connection state: %w", err)
	}
	return nil
}

func runnerConnectionActive(pid int, machineID string) bool {
	path, err := runnerConnectionStatePath()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var state runnerConnectionState
	if json.Unmarshal(raw, &state) != nil {
		return false
	}
	return state.PID == pid && state.MachineID == machineID
}

func clearRunnerConnectionState(pid int) {
	path, err := runnerConnectionStatePath()
	if err != nil {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var state runnerConnectionState
	if json.Unmarshal(raw, &state) != nil || state.PID != pid {
		return
	}
	_ = os.Remove(path)
}
