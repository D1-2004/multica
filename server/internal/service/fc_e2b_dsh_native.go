package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

const DSHNativeGatewayPort = 33124

var dshGatewayLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// Gateway addresses are derived from deployment configuration and a persisted
// Host, never from a browser-provided redirect or a sandbox's returned URL.
func dshNativeGatewayAddress(config FCE2BConfig, host dshhost.Host) (string, string, error) {
	denied := errors.New("DSH native gateway configuration is unavailable")
	if host.State != "running" || host.WorkspaceID == uuid.Nil || host.AgentID == uuid.Nil || host.Generation < 1 || (len(host.SandboxID) > 57 || !dshGatewayLabel.MatchString(host.SandboxID)) {
		return "", "", denied
	}
	authority, err := url.Parse(config.ServerURL)
	if err != nil || authority.Scheme != "https" || authority.Host == "" || authority.User != nil || authority.RawQuery != "" || authority.Fragment != "" || (authority.Path != "" && authority.Path != "/") {
		return "", "", denied
	}
	domain := config.Domain
	if len(domain) > 190 || !strings.Contains(domain, ".") {
		return "", "", denied
	}
	for _, label := range strings.Split(domain, ".") {
		if !dshGatewayLabel.MatchString(label) {
			return "", "", denied
		}
	}
	origin := fmt.Sprintf("https://%d-%s.%s", DSHNativeGatewayPort, host.SandboxID, domain)
	return origin, strings.TrimSuffix(authority.String(), "/"), nil
}

type dshNativeGatewayReceipt struct {
	Version     int    `json:"version"`
	Ready       bool   `json:"ready"`
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	Generation  int64  `json:"generation"`
	SandboxID   string `json:"sandbox_id"`
	Port        int    `json:"port"`
	Authority   string `json:"authority"`
	Origin      string `json:"origin"`
}

func validateDSHNativeGatewayReceipt(out string, host dshhost.Host, origin, authority string) error {
	var receipt dshNativeGatewayReceipt
	if len(out) > 4096 || json.Unmarshal([]byte(out), &receipt) != nil || receipt.Version != 1 || !receipt.Ready || receipt.WorkspaceID != host.WorkspaceID.String() || receipt.AgentID != host.AgentID.String() || receipt.Generation != host.Generation || receipt.SandboxID != host.SandboxID || receipt.Port != DSHNativeGatewayPort || receipt.Authority != authority || receipt.Origin != origin {
		return errors.New("DSH native gateway did not confirm the current employee Host and authority")
	}
	return nil
}

// DSHNativeGatewayURL checks the existing supervisor without creating, renewing
// or replacing a writer. Older images without this command fail closed.
func (l *FCE2BLauncher) DSHNativeGatewayURL(ctx context.Context, host dshhost.Host) (string, error) {
	l = l.withCurrentConfig()
	if l == nil || l.Runner == nil {
		return "", errors.New("DSH native gateway is unavailable")
	}
	origin, authority, err := dshNativeGatewayAddress(l.Config, host)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := l.runE2BCommand(ctx, []string{"sandbox", "exec", "--user", "user", "-e", "LD_PRELOAD=", "-e", "LD_LIBRARY_PATH=", "-e", "PYTHONPATH=", "-e", "PYTHONHOME=",
		"-e", "DSH_HOME=" + dshhost.MountPath + "/home",
		"-e", "MULTICA_DSH_WORKSPACE_ID=" + host.WorkspaceID.String(),
		"-e", "MULTICA_DSH_AGENT_ID=" + host.AgentID.String(),
		"-e", "MULTICA_DSH_HOST_GENERATION=" + strconv.FormatInt(host.Generation, 10), host.SandboxID, "--", "/usr/local/libexec/multica-dsh-host", "--gateway-health"})
	if err != nil {
		return "", errors.New("DSH native gateway readiness is unconfirmed")
	}
	if err := validateDSHNativeGatewayReceipt(out, host, origin, authority); err != nil {
		return "", err
	}
	return origin, nil
}
