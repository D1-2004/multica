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
	"strings"
	"sync"
	"time"
)

type runnerServerBinding struct {
	ServerURL string `json:"server_url"`
	MachineID string `json:"machine_id"`
}

type runnerConfig struct {
	ServerURL  string                `json:"server_url,omitempty"`
	MachineID  string                `json:"machine_id,omitempty"`
	Servers    []runnerServerBinding `json:"servers,omitempty"`
	PublicKey  string                `json:"public_key"`
	PrivateKey string                `json:"private_key"`
	Roots      []string              `json:"roots"`
}

type runnerConnectionState struct {
	PID         int       `json:"pid"`
	MachineIDs  []string  `json:"machine_ids,omitempty"`
	MachineID   string    `json:"machine_id,omitempty"`
	ConnectedAt time.Time `json:"connected_at"`
}

var runnerConnectionStateMu sync.Mutex

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
	if _, err := cfg.bindings(); err != nil {
		return runnerConfig{}, err
	}
	return cfg, nil
}

func (cfg runnerConfig) bindings() ([]runnerServerBinding, error) {
	if len(cfg.Servers) > 0 {
		out := make([]runnerServerBinding, 0, len(cfg.Servers))
		seen := make(map[string]struct{}, len(cfg.Servers))
		for _, binding := range cfg.Servers {
			serverURL, err := normalizeRunnerServerURL(binding.ServerURL)
			if err != nil {
				return nil, fmt.Errorf("Runner server %q: %w", binding.ServerURL, err)
			}
			if strings.TrimSpace(binding.MachineID) == "" {
				return nil, fmt.Errorf("Runner server %s is missing a machine id", serverURL)
			}
			if _, exists := seen[serverURL]; exists {
				return nil, fmt.Errorf("Runner config lists %s more than once", serverURL)
			}
			seen[serverURL] = struct{}{}
			out = append(out, runnerServerBinding{ServerURL: serverURL, MachineID: binding.MachineID})
		}
		return out, nil
	}
	if strings.TrimSpace(cfg.ServerURL) == "" && strings.TrimSpace(cfg.MachineID) == "" {
		return nil, nil
	}
	serverURL, err := normalizeRunnerServerURL(cfg.ServerURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.MachineID) == "" {
		return nil, fmt.Errorf("Runner server %s is missing a machine id", serverURL)
	}
	return []runnerServerBinding{{ServerURL: serverURL, MachineID: cfg.MachineID}}, nil
}

func (cfg runnerConfig) bindingForURL(serverURL string) (runnerServerBinding, bool, error) {
	serverURL, err := normalizeRunnerServerURL(serverURL)
	if err != nil {
		return runnerServerBinding{}, false, err
	}
	bindings, err := cfg.bindings()
	if err != nil {
		return runnerServerBinding{}, false, err
	}
	for _, binding := range bindings {
		if binding.ServerURL == serverURL {
			return binding, true, nil
		}
	}
	return runnerServerBinding{}, false, nil
}

func (cfg *runnerConfig) upsertBinding(serverURL, machineID string) error {
	serverURL, err := normalizeRunnerServerURL(serverURL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(machineID) == "" {
		return fmt.Errorf("Runner server %s is missing a machine id", serverURL)
	}
	bindings, err := cfg.bindings()
	if err != nil {
		return err
	}
	replaced := false
	for i, binding := range bindings {
		if binding.ServerURL == serverURL {
			bindings[i].MachineID = machineID
			replaced = true
			break
		}
	}
	if !replaced {
		bindings = append(bindings, runnerServerBinding{ServerURL: serverURL, MachineID: machineID})
	}
	cfg.Servers = bindings
	cfg.ServerURL = ""
	cfg.MachineID = ""
	return nil
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

func (state runnerConnectionState) machineIDs() []string {
	if len(state.MachineIDs) > 0 {
		return append([]string(nil), state.MachineIDs...)
	}
	if state.MachineID != "" {
		return []string{state.MachineID}
	}
	return nil
}

func readRunnerConnectionState() (runnerConnectionState, error) {
	path, err := runnerConnectionStatePath()
	if err != nil {
		return runnerConnectionState{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return runnerConnectionState{}, err
	}
	var state runnerConnectionState
	if err := json.Unmarshal(raw, &state); err != nil {
		return runnerConnectionState{}, err
	}
	return state, nil
}

func persistRunnerConnectionState(state runnerConnectionState) error {
	path, err := runnerConnectionStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Runner state directory: %w", err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode Runner connection state: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write Runner connection state: %w", err)
	}
	return nil
}

func writeRunnerConnectionState(machineID string) error {
	runnerConnectionStateMu.Lock()
	defer runnerConnectionStateMu.Unlock()
	state, err := readRunnerConnectionState()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read Runner connection state: %w", err)
	}
	if state.PID != os.Getpid() {
		state = runnerConnectionState{PID: os.Getpid()}
	}
	ids := state.machineIDs()
	found := false
	for _, id := range ids {
		if id == machineID {
			found = true
			break
		}
	}
	if !found {
		ids = append(ids, machineID)
	}
	return persistRunnerConnectionState(runnerConnectionState{
		PID:         os.Getpid(),
		MachineIDs:  ids,
		ConnectedAt: time.Now().UTC(),
	})
}

func runnerConnectionActive(pid int, machineID string) bool {
	runnerConnectionStateMu.Lock()
	defer runnerConnectionStateMu.Unlock()
	return runnerConnectionActiveLocked(pid, machineID)
}

func runnerConnectionActiveLocked(pid int, machineID string) bool {
	state, err := readRunnerConnectionState()
	if err != nil {
		return false
	}
	if state.PID != pid {
		return false
	}
	for _, id := range state.machineIDs() {
		if id == machineID {
			return true
		}
	}
	return false
}

func runnerAllBindingsConnected(pid int, cfg runnerConfig) bool {
	bindings, err := cfg.bindings()
	if err != nil || len(bindings) == 0 {
		return false
	}
	runnerConnectionStateMu.Lock()
	defer runnerConnectionStateMu.Unlock()
	for _, binding := range bindings {
		if !runnerConnectionActiveLocked(pid, binding.MachineID) {
			return false
		}
	}
	return true
}

func clearRunnerConnectionState(pid int) {
	clearRunnerConnectionMachine(pid, "")
}

func clearRunnerConnectionMachine(pid int, machineID string) {
	runnerConnectionStateMu.Lock()
	defer runnerConnectionStateMu.Unlock()
	path, err := runnerConnectionStatePath()
	if err != nil {
		return
	}
	state, err := readRunnerConnectionState()
	if err != nil || state.PID != pid {
		return
	}
	if machineID == "" {
		_ = os.Remove(path)
		return
	}
	ids := make([]string, 0, len(state.machineIDs()))
	for _, id := range state.machineIDs() {
		if id != machineID {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		_ = os.Remove(path)
		return
	}
	_ = persistRunnerConnectionState(runnerConnectionState{
		PID:         pid,
		MachineIDs:  ids,
		ConnectedAt: state.ConnectedAt,
	})
}
