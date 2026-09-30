package remotemcp

// Fork-only (dt-fde-multica). The upstream files of this package are
// verbatim copies of multica-ai/multica; fork additions live in files of
// their own so a later upstream sync stays conflict-free.
//
// ExternalClient talks to official remote MCP servers and their OAuth
// authorization servers from the Multica server. Unlike NewSecureHTTPClient
// it honours HTTP(S)_PROXY / NO_PROXY, because deployments may only reach the
// internet through an egress proxy. Every request is pinned to a fixed host
// allowlist (the catalog app's hosts). A direct connection additionally
// refuses non-public addresses; a proxied one cannot see the target address,
// so the host allowlist is the boundary there.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ExternalTimeout bounds one request of an ExternalClient.
const ExternalTimeout = 45 * time.Second

// ExternalClient is safe for concurrent use.
type ExternalClient struct {
	hosts []string
	http  *http.Client
}

// NewExternalClient returns a proxy-aware client limited to hosts. A host
// entry is an exact hostname (default HTTPS port only) or host:port.
func NewExternalClient(hosts []string) *ExternalClient {
	normalized := normalizeExternalHosts(hosts)
	return newExternalClient(normalized, newExternalTransport(normalized))
}

// NewExternalClientWithTransport is NewExternalClient over another transport
// (tests use an httptest TLS transport). The host allowlist still applies to
// every request.
func NewExternalClientWithTransport(hosts []string, base http.RoundTripper) *ExternalClient {
	return newExternalClient(normalizeExternalHosts(hosts), base)
}

func newExternalClient(hosts []string, base http.RoundTripper) *ExternalClient {
	return &ExternalClient{
		hosts: hosts,
		http: &http.Client{
			Timeout:   ExternalTimeout,
			Transport: externalHostGuard{hosts: hosts, next: base},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("remote MCP redirects are not allowed")
			},
		},
	}
}

// Hosts returns the client's host allowlist.
func (c *ExternalClient) Hosts() []string { return append([]string(nil), c.hosts...) }

// HTTPClient returns the underlying HTTP client.
func (c *ExternalClient) HTTPClient() *http.Client { return c.http }

// CheckURL parses raw and requires https, no userinfo or fragment, and an
// allowed host. A query is allowed (authorization endpoints may carry one).
func (c *ExternalClient) CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("URL must be https without userinfo or fragment")
	}
	if !externalHostAllowed(u, c.hosts) {
		return nil, fmt.Errorf("host %q is outside the connector's host allowlist", u.Host)
	}
	return u, nil
}

type externalHostGuard struct {
	hosts []string
	next  http.RoundTripper
}

func (g externalHostGuard) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL == nil || request.URL.Scheme != "https" || request.URL.User != nil || !externalHostAllowed(request.URL, g.hosts) {
		if request.Body != nil {
			_ = request.Body.Close()
		}
		return nil, errors.New("remote MCP request host is outside the connector's host allowlist")
	}
	return g.next.RoundTrip(request)
}

func newExternalTransport(hosts []string) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	// The proxies configured for these hosts are operator-chosen; the dialer
	// connects to them without the public-address check (a corporate proxy
	// usually has a private address).
	proxies := map[string]bool{}
	for _, host := range hosts {
		target := &url.URL{Scheme: "https", Host: host}
		if proxyURL, err := http.ProxyFromEnvironment(&http.Request{URL: target}); err == nil && proxyURL != nil {
			proxies[proxyDialAddress(proxyURL)] = true
		}
	}
	dialer := &net.Dialer{Timeout: ConnectTimeout, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if proxies[strings.ToLower(address)] {
			return dialer.DialContext(ctx, network, address)
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		target := &url.URL{Scheme: "https", Host: address}
		if !externalHostAllowed(target, hosts) {
			return nil, errors.New("remote MCP host is outside the connector's host allowlist")
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("resolve remote MCP host: %w", err)
		}
		devOrigin := isDevOrigin(target)
		for _, candidate := range addresses {
			candidate = candidate.Unmap()
			if !devOrigin && !isPublicAddress(candidate) {
				return nil, errors.New("remote MCP host resolved to a non-public address")
			}
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
			if dialErr == nil {
				return connection, nil
			}
		}
		return nil, errors.New("connect to remote MCP host failed")
	}
	return transport
}

func proxyDialAddress(proxyURL *url.URL) string {
	port := proxyURL.Port()
	if port == "" {
		switch proxyURL.Scheme {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return strings.ToLower(net.JoinHostPort(proxyURL.Hostname(), port))
}

// externalHostAllowed matches u against exact host entries. A bare hostname
// matches only the default HTTPS port; host:port matches that port.
func externalHostAllowed(u *url.URL, hosts []string) bool {
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	port := u.Port()
	for _, entry := range hosts {
		entryHost, entryPort, err := net.SplitHostPort(entry)
		if err != nil {
			if host == entry && (port == "" || port == "443") {
				return true
			}
			continue
		}
		if host == entryHost && port == entryPort {
			return true
		}
	}
	return false
}

func normalizeExternalHosts(hosts []string) []string {
	out := make([]string, 0, len(hosts))
	seen := map[string]bool{}
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if h, port, err := net.SplitHostPort(host); err == nil {
			host = net.JoinHostPort(strings.TrimSuffix(h, "."), port)
		} else {
			host = strings.TrimSuffix(host, ".")
		}
		if host == "" || strings.ContainsAny(host, "/@?#*") || seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	return out
}
