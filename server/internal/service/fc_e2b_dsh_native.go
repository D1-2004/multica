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

func (l *FCE2BLauncher) DSHNativeProxyAddress(host dshhost.Host) (string, string, error) {
	l = l.withCurrentConfig()
	if l == nil {
		return "", "", errors.New("DSH native gateway unavailable")
	}
	return dshNativeGatewayAddress(l.Config, host)
}

var dshGatewayLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// Gateway addresses are derived from deployment configuration and a persisted
// Host, never from a browser-provided redirect or a sandbox's returned URL.
func dshNativeGatewayAddress(config FCE2BConfig, host dshhost.Host) (string, string, error) {
	denied := errors.New("DSH native gateway configuration is unavailable")
	if host.State != "running" || host.WorkspaceID == uuid.Nil || host.AgentID == uuid.Nil || host.Generation < 1 || (len(host.SandboxID) > 57 || !dshGatewayLabel.MatchString(host.SandboxID)) {
		return "", "", denied
	}
	// Task traffic may use a different environment's ingress relay. Native
	// capabilities must be checked by this deployment's own application.
	authority, err := url.Parse(config.DSHNativeAuthority)
	if err != nil || len(config.DSHNativeAuthority) > 2048 || authority.Scheme != "https" || authority.Host == "" || authority.Host != authority.Hostname() || authority.User != nil || authority.RawQuery != "" || authority.ForceQuery || authority.Fragment != "" || (authority.Path != "" && authority.Path != "/") {
		return "", "", denied
	}
	for _, label := range strings.Split(authority.Host, ".") {
		if !dshGatewayLabel.MatchString(label) {
			return "", "", denied
		}
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
	PublicKey   string `json:"public_key"`
	Origin      string `json:"origin"`
}

func validateDSHNativeGatewayReceipt(out string, host dshhost.Host, origin, authority, publicKey string) error {
	var receipt dshNativeGatewayReceipt
	if len(out) > 4096 || json.Unmarshal([]byte(out), &receipt) != nil || receipt.Version != 3 || !receipt.Ready || receipt.WorkspaceID != host.WorkspaceID.String() || receipt.AgentID != host.AgentID.String() || receipt.Generation != host.Generation || receipt.SandboxID != host.SandboxID || receipt.Port != DSHNativeGatewayPort || receipt.Authority != authority || receipt.Origin != origin || receipt.PublicKey != publicKey || len(publicKey) != 64 {
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
	out, err := l.dshGatewayControl(ctx, host, "--gateway-health")
	if err != nil {
		return "", errors.New("DSH native gateway readiness is unconfirmed")
	}
	if err := validateDSHNativeGatewayReceipt(out, host, origin, authority, l.nativeAuthority.publicKey()); err != nil {
		return "", err
	}
	return origin, nil
}

func (l *FCE2BLauncher) dshGatewayControl(ctx context.Context, host dshhost.Host, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return l.runE2BCommand(ctx, []string{"sandbox", "exec", "--user", "user", "-e", "LD_PRELOAD=", "-e", "LD_LIBRARY_PATH=", "-e", "PYTHONPATH=", "-e", "PYTHONHOME=",
		"-e", "DSH_HOME=" + dshhost.MountPath + "/home",
		"-e", "MULTICA_DSH_WORKSPACE_ID=" + host.WorkspaceID.String(),
		"-e", "MULTICA_DSH_AGENT_ID=" + host.AgentID.String(),
		"-e", "MULTICA_DSH_HOST_GENERATION=" + strconv.FormatInt(host.Generation, 10), host.SandboxID, "--", "/usr/local/libexec/multica-dsh-host", command})
}
