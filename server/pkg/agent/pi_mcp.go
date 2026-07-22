package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	piMCPConfigPathEnv    = "MULTICA_PI_MCP_CONFIG_PATH"
	piMCPExtensionPathEnv = "MULTICA_PI_MCP_EXTENSION_PATH"
	piMCPConfigSchema     = 1
	piMCPMaxConfigBytes   = 1024 * 1024
)

var (
	piMCPServerNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	piMCPEnvNameRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	piMCPHeaderNameRE = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")
)

// piMCPConfig is the private, versioned contract between the Multica Pi
// backend and the root-owned Pi extension shipped in the FC/E2B image. Agent
// mcp_config is never handed to the extension verbatim: the daemon validates
// and normalises the public Claude-style document into this smaller schema so
// transport selection is explicit and the extension never guesses or falls
// back between protocols.
type piMCPConfig struct {
	SchemaVersion int           `json:"schemaVersion"`
	Servers       []piMCPServer `json:"servers"`
}

type piMCPServer struct {
	Name             string            `json:"name"`
	Transport        string            `json:"transport"`
	Command          string            `json:"command,omitempty"`
	Args             []string          `json:"args,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	URL              string            `json:"url,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	RequestTimeoutMS int               `json:"requestTimeoutMs,omitempty"`
}

type piMCPSourceServer struct {
	Type     string            `json:"type,omitempty"`
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Enabled  *bool             `json:"enabled,omitempty"`
	Disabled *bool             `json:"disabled,omitempty"`
	Timeout  *int              `json:"timeout,omitempty"`
}

type piMCPNativeSourceServer struct {
	Type        string            `json:"type,omitempty"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
	Timeout     *int              `json:"timeout,omitempty"`
}

func normalizePiMCPConfig(raw json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, errors.New("pi mcp_config is not a managed JSON object")
	}
	if len(trimmed) > piMCPMaxConfigBytes {
		return nil, errors.New("pi mcp_config exceeds the 1 MiB limit")
	}

	var source struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
		MCP        map[string]json.RawMessage `json:"mcp"`
	}
	if err := strictDecodeSingleJSON(trimmed, &source); err != nil {
		return nil, fmt.Errorf("pi mcp_config: %w", err)
	}
	if source.MCPServers == nil && source.MCP == nil {
		// `{}` is the API's explicit managed-empty state. It must not inherit
		// any ambient MCP configuration from the runtime.
		source.MCPServers = map[string]json.RawMessage{}
	}

	for name := range source.MCPServers {
		if _, duplicate := source.MCP[name]; duplicate {
			return nil, fmt.Errorf("pi mcp_config: server %q is defined in both mcpServers and mcp", name)
		}
	}

	names := make([]string, 0, len(source.MCPServers)+len(source.MCP))
	for name := range source.MCPServers {
		names = append(names, name)
	}
	for name := range source.MCP {
		names = append(names, name)
	}
	sort.Strings(names)

	servers := make([]piMCPServer, 0, len(names))
	for _, name := range names {
		if !piMCPServerNameRE.MatchString(name) {
			return nil, fmt.Errorf("pi mcp_config: server %q has an invalid name", name)
		}
		var server piMCPServer
		var enabled bool
		var err error
		if raw, ok := source.MCPServers[name]; ok {
			server, enabled, err = normalizePiMCPClaudeServer(name, raw)
		} else {
			server, enabled, err = normalizePiMCPNativeServer(name, source.MCP[name])
		}
		if err != nil {
			return nil, err
		}
		if enabled {
			servers = append(servers, server)
		}
	}

	data, err := json.Marshal(piMCPConfig{SchemaVersion: piMCPConfigSchema, Servers: servers})
	if err != nil {
		return nil, fmt.Errorf("pi mcp_config: marshal canonical config: %w", err)
	}
	if len(data) > piMCPMaxConfigBytes {
		return nil, errors.New("pi canonical mcp_config exceeds the 1 MiB limit")
	}
	return data, nil
}

func normalizePiMCPClaudeServer(name string, raw json.RawMessage) (piMCPServer, bool, error) {
	var source piMCPSourceServer
	if err := strictDecodeSingleJSON(raw, &source); err != nil {
		return piMCPServer{}, false, fmt.Errorf("pi mcp_config: server %q: %w", name, err)
	}
	if source.Enabled != nil && source.Disabled != nil && *source.Enabled == *source.Disabled {
		return piMCPServer{}, false, fmt.Errorf("pi mcp_config: server %q has conflicting enabled and disabled values", name)
	}
	if (source.Enabled != nil && !*source.Enabled) || (source.Disabled != nil && *source.Disabled) {
		return piMCPServer{}, false, nil
	}
	server, err := normalizePiMCPServer(name, source)
	return server, err == nil, err
}

func normalizePiMCPNativeServer(name string, raw json.RawMessage) (piMCPServer, bool, error) {
	var source piMCPNativeSourceServer
	if err := strictDecodeSingleJSON(raw, &source); err != nil {
		return piMCPServer{}, false, fmt.Errorf("pi mcp_config: native server %q: %w", name, err)
	}
	if source.Enabled != nil && !*source.Enabled {
		return piMCPServer{}, false, nil
	}
	timeoutMS := 0
	if source.Timeout != nil {
		if *source.Timeout < 1000 || *source.Timeout > 600000 {
			return piMCPServer{}, false, fmt.Errorf("pi mcp_config: native server %q timeout must be in 1000..600000 milliseconds", name)
		}
		timeoutMS = *source.Timeout
	}

	var canonicalSource piMCPSourceServer
	switch strings.ToLower(strings.TrimSpace(source.Type)) {
	case "local":
		if len(source.Command) == 0 {
			return piMCPServer{}, false, fmt.Errorf("pi mcp_config: native server %q local command is empty", name)
		}
		canonicalSource = piMCPSourceServer{
			Type:    "stdio",
			Command: source.Command[0],
			Args:    source.Command[1:],
			Env:     source.Environment,
		}
	case "remote":
		canonicalSource = piMCPSourceServer{
			Type:    "streamable-http",
			URL:     source.URL,
			Headers: source.Headers,
		}
	default:
		return piMCPServer{}, false, fmt.Errorf("pi mcp_config: native server %q type must be local or remote", name)
	}
	server, err := normalizePiMCPServer(name, canonicalSource)
	if err != nil {
		return piMCPServer{}, false, err
	}
	server.RequestTimeoutMS = timeoutMS
	return server, true, nil
}

func normalizePiMCPServer(name string, source piMCPSourceServer) (piMCPServer, error) {
	typeName := strings.ToLower(strings.TrimSpace(source.Type))
	command := strings.TrimSpace(source.Command)
	remoteURL := strings.TrimSpace(source.URL)
	if (command == "") == (remoteURL == "") {
		return piMCPServer{}, fmt.Errorf("pi mcp_config: server %q must set exactly one of command or url", name)
	}
	if source.Timeout != nil && (*source.Timeout <= 0 || *source.Timeout > 600) {
		return piMCPServer{}, fmt.Errorf("pi mcp_config: server %q timeout must be in 1..600 seconds", name)
	}
	timeoutMS := 0
	if source.Timeout != nil {
		timeoutMS = *source.Timeout * 1000
	}

	if command != "" {
		switch typeName {
		case "", "stdio", "local":
		default:
			return piMCPServer{}, fmt.Errorf("pi mcp_config: server %q command transport type must be stdio", name)
		}
		if strings.IndexByte(command, 0) >= 0 {
			return piMCPServer{}, fmt.Errorf("pi mcp_config: server %q command contains a NUL byte", name)
		}
		for _, arg := range source.Args {
			if strings.IndexByte(arg, 0) >= 0 {
				return piMCPServer{}, fmt.Errorf("pi mcp_config: server %q args contain a NUL byte", name)
			}
		}
		for key, value := range source.Env {
			if !piMCPEnvNameRE.MatchString(key) {
				return piMCPServer{}, fmt.Errorf("pi mcp_config: server %q has an invalid env name", name)
			}
			if strings.IndexByte(value, 0) >= 0 {
				return piMCPServer{}, fmt.Errorf("pi mcp_config: server %q env contains a NUL byte", name)
			}
		}
		return piMCPServer{
			Name:             name,
			Transport:        "stdio",
			Command:          command,
			Args:             source.Args,
			Env:              source.Env,
			RequestTimeoutMS: timeoutMS,
		}, nil
	}

	transport := "streamable-http"
	switch typeName {
	case "", "http", "remote", "streamable-http", "http_streamable":
	case "sse":
		transport = "sse"
	default:
		return piMCPServer{}, fmt.Errorf("pi mcp_config: server %q url transport type must be streamable-http or sse", name)
	}
	parsedURL, err := url.Parse(remoteURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.User != nil {
		return piMCPServer{}, fmt.Errorf("pi mcp_config: server %q has an invalid http(s) url", name)
	}
	for key, value := range source.Headers {
		if !piMCPHeaderNameRE.MatchString(key) || !validPiMCPHeaderValue(value) {
			return piMCPServer{}, fmt.Errorf("pi mcp_config: server %q has an invalid header", name)
		}
	}
	seenHeaderNames := make(map[string]struct{}, len(source.Headers))
	for key := range source.Headers {
		lower := strings.ToLower(key)
		if _, duplicate := seenHeaderNames[lower]; duplicate {
			return piMCPServer{}, fmt.Errorf("pi mcp_config: server %q has duplicate header names", name)
		}
		seenHeaderNames[lower] = struct{}{}
	}
	return piMCPServer{
		Name:             name,
		Transport:        transport,
		URL:              remoteURL,
		Headers:          source.Headers,
		RequestTimeoutMS: timeoutMS,
	}, nil
}

func validPiMCPHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return false
		}
	}
	return true
}

func strictDecodeSingleJSON(raw []byte, target any) error {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return errors.New("multiple JSON values are not allowed")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := scanUniqueJSONValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err == nil {
		return errors.New("multiple JSON values are not allowed")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func scanUniqueJSONValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key must be a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanUniqueJSONValue(dec); err != nil {
				return err
			}
		}
		closing, err := dec.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("invalid JSON object closing delimiter")
		}
	case '[':
		for dec.More() {
			if err := scanUniqueJSONValue(dec); err != nil {
				return err
			}
		}
		closing, err := dec.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("invalid JSON array closing delimiter")
		}
	default:
		return errors.New("unexpected JSON closing delimiter")
	}
	return nil
}

func writePiMCPConfigToTemp(raw json.RawMessage, taskTempDir string) (string, error) {
	data, err := normalizePiMCPConfig(raw)
	if err != nil {
		return "", err
	}
	taskTempDir, err = validatePiMCPTaskTempDir(taskTempDir)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(taskTempDir, "multica-pi-mcp-*")
	if err != nil {
		return "", fmt.Errorf("create pi mcp config temp dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("secure pi mcp config temp dir: %w", err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("write pi mcp config temp file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("secure pi mcp config temp file: %w", err)
	}
	return path, nil
}

func validatePiMCPTaskTempDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("pi managed MCP requires a task-scoped TMPDIR")
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("pi managed MCP TMPDIR must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("pi managed MCP TMPDIR is unavailable: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("pi managed MCP TMPDIR must be an owner-only directory")
	}
	return path, nil
}

func cleanupPiMCPConfigTemp(path string) {
	if path == "" {
		return
	}
	dir := filepath.Dir(path)
	if strings.HasPrefix(filepath.Base(dir), "multica-pi-mcp-") {
		_ = os.RemoveAll(dir)
	}
}

func validatePiMCPExtensionPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("pi managed MCP requires %s", piMCPExtensionPathEnv)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("pi managed MCP extension path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("pi managed MCP extension is unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("pi managed MCP extension must be a regular file")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return "", errors.New("pi managed MCP extension must not be group- or world-writable")
	}
	return path, nil
}

func envValue(extra map[string]string, key string) string {
	if value, ok := extra[key]; ok {
		return value
	}
	return os.Getenv(key)
}

func replaceEnvValue(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	if value != "" {
		out = append(out, prefix+value)
	}
	return out
}

func addPiManagedExtensionArg(args []string, extensionPath string) []string {
	if len(args) == 0 {
		return []string{"--no-extensions", "--extension", extensionPath}
	}
	out := make([]string, 0, len(args)+3)
	out = append(out, args[:len(args)-1]...)
	out = append(out, "--no-extensions", "--extension", extensionPath, args[len(args)-1])
	return out
}
