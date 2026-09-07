package dshplugin

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// A package source is caller-supplied, so fetching one is a server-side request
// to an address a workspace member chose. Without a guard that is a
// server-side request forgery primitive: an https URL that redirects to
// 127.0.0.1 or a link-local metadata address would be fetched by this process,
// from inside the network, before any plugin validation runs.
//
// Two defences, because either alone is bypassable:
//   - Control returns an error for any non-public address the resolver hands
//     the dialer, which covers DNS names that resolve to private space and
//     closes the rebinding window between resolution and connection.
//   - CheckRedirect re-applies the scheme rule on every hop, so an https URL
//     cannot bounce to http or to a different protocol.
const maxRedirects = 5

func newSafeHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("refusing to dial %q", address)
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("refusing to dial %q", address)
			}
			if !isPublicIP(ip) {
				return fmt.Errorf("refusing to reach the non-public address %s", ip)
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          8,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing to follow a redirect to %s", req.URL.Scheme)
			}
			return nil
		},
	}
}

// isPublicIP reports whether an address is routable on the public internet.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if ip.IsPrivate() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		// 100.64.0.0/10 carrier-grade NAT, which reaches cloud metadata on
		// some providers.
		case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127:
			return false
		// 192.0.0.0/24 IETF protocol assignments.
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 0:
			return false
		// 198.18.0.0/15 benchmarking.
		case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19):
			return false
		}
		return true
	}
	// IPv6 unique-local addresses (fc00::/7).
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc {
		return false
	}
	// IPv4-mapped IPv6 is handled by To4 above; anything left is public.
	return true
}

// contextWithFetchTimeout bounds one fetch independently of the caller's
// deadline, so a slow mirror cannot hold a request open for the whole
// handler timeout.
func contextWithFetchTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}
