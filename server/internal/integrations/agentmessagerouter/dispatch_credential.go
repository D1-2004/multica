package agentmessagerouter

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
)

const dispatchDomain = "multica-agent-dispatch:v1:"

var dispatchKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,31}$`)

type DispatchKeyring struct {
	currentKeyID string
	keys         map[string][]byte
	metrics      *obsmetrics.BusinessMetrics
}

func ParseDispatchKeyring(raw, currentKeyID string) (*DispatchKeyring, error) {
	currentKeyID = strings.TrimSpace(currentKeyID)
	if !dispatchKeyIDPattern.MatchString(currentKeyID) {
		return nil, errors.New("agent dispatch current key id is invalid")
	}
	keys := make(map[string][]byte)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		keyID, encoded, ok := strings.Cut(entry, ":")
		keyID = strings.TrimSpace(keyID)
		encoded = strings.TrimSpace(encoded)
		if !ok || !dispatchKeyIDPattern.MatchString(keyID) || encoded == "" {
			return nil, errors.New("agent dispatch key entry is invalid")
		}
		if _, exists := keys[keyID]; exists {
			return nil, fmt.Errorf("agent dispatch key id %q is duplicated", keyID)
		}
		key, err := decodeDispatchKey(encoded)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("agent dispatch key %q must decode to 32 bytes", keyID)
		}
		keys[keyID] = key
	}
	if len(keys) == 0 {
		return nil, errors.New("agent dispatch keyring is empty")
	}
	if _, ok := keys[currentKeyID]; !ok {
		return nil, errors.New("agent dispatch current key is missing")
	}
	return &DispatchKeyring{currentKeyID: currentKeyID, keys: keys}, nil
}

func decodeDispatchKey(encoded string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		decoded, err := encoding.DecodeString(encoded)
		if err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64 dispatch key")
}

func (k *DispatchKeyring) CurrentKeyID() string {
	if k == nil {
		return ""
	}
	return k.currentKeyID
}

// KeyFingerprints returns non-secret key summaries for operational comparison
// with Router. The raw key material and derived delivery credentials stay hidden.
func (k *DispatchKeyring) KeyFingerprints() []string {
	if k == nil {
		return nil
	}
	keyIDs := make([]string, 0, len(k.keys))
	for keyID := range k.keys {
		keyIDs = append(keyIDs, keyID)
	}
	sort.Strings(keyIDs)
	fingerprints := make([]string, 0, len(keyIDs))
	for _, keyID := range keyIDs {
		digest := sha256.Sum256(k.keys[keyID])
		fingerprints = append(fingerprints,
			keyID+"="+base64.RawURLEncoding.EncodeToString(digest[:])[:12])
	}
	return fingerprints
}

func (k *DispatchKeyring) SetMetrics(businessMetrics *obsmetrics.BusinessMetrics) {
	if k == nil {
		return
	}
	k.metrics = businessMetrics
}

func (k *DispatchKeyring) GenerateEndpointID(random io.Reader) (string, error) {
	if k == nil || random == nil || k.currentKeyID == "" {
		return "", errors.New("agent dispatch keyring is not configured")
	}
	value := make([]byte, 16)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", fmt.Errorf("generate dispatch endpoint id: %w", err)
	}
	return k.currentKeyID + "_" + base64.RawURLEncoding.EncodeToString(value), nil
}

func (k *DispatchKeyring) DeriveDeliverySecret(endpointID string) (string, error) {
	keyID, err := parseEndpointID(endpointID)
	if err != nil {
		if k != nil {
			k.metrics.RecordDispatchCredentialDerive("invalid_endpoint", "invalid")
		}
		return "", err
	}
	if k == nil {
		return "", errors.New("agent dispatch keyring is not configured")
	}
	key, ok := k.keys[keyID]
	if !ok {
		k.metrics.RecordDispatchCredentialDerive("unknown_key", "unknown")
		return "", errors.New("agent dispatch endpoint uses an unknown key id")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(dispatchDomain + endpointID))
	secret := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	k.metrics.RecordDispatchCredentialDerive("success", keyID)
	return secret, nil
}

func (k *DispatchKeyring) VerifyDeliverySecret(endpointID, actual string) bool {
	expected, err := k.DeriveDeliverySecret(endpointID)
	if err != nil || actual == "" || len(actual) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

func BuildDispatchURL(publicOrigin, endpointID string) (string, error) {
	if _, err := parseEndpointID(endpointID); err != nil {
		return "", err
	}
	origin, err := canonicalHTTPSOrigin(publicOrigin)
	if err != nil {
		return "", err
	}
	origin.Path = "/api/webhooks/agent-dispatch/" + endpointID
	return origin.String(), nil
}

func dispatchPathForEndpointID(endpointID string) (string, error) {
	if _, err := parseEndpointID(endpointID); err != nil {
		return "", err
	}
	return "/api/webhooks/agent-dispatch/" + endpointID, nil
}

func endpointIDFromDispatchPath(dispatchPath string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(dispatchPath))
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery || parsed.Opaque != "" {
		return "", errors.New("agent dispatch path is invalid")
	}
	const prefix = "/api/webhooks/agent-dispatch/"
	if !strings.HasPrefix(parsed.Path, prefix) {
		return "", errors.New("agent dispatch path is invalid")
	}
	endpointID := strings.TrimPrefix(parsed.Path, prefix)
	expectedPath, err := dispatchPathForEndpointID(endpointID)
	if err != nil || parsed.Path != expectedPath {
		return "", errors.New("agent dispatch path is invalid")
	}
	return endpointID, nil
}

func isDispatchURLForEndpoint(dispatchURL, endpointID string) bool {
	parsed, err := url.Parse(strings.TrimSpace(dispatchURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery || parsed.Opaque != "" {
		return false
	}
	expectedPath, err := dispatchPathForEndpointID(endpointID)
	return err == nil && parsed.Path == expectedPath
}

func isCanonicalDispatchURLForEndpoint(dispatchURL, endpointID string) bool {
	parsed, err := url.Parse(strings.TrimSpace(dispatchURL))
	return err == nil && parsed.Scheme == "https" && isDispatchURLForEndpoint(dispatchURL, endpointID)
}

func canonicalHTTPSOrigin(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery ||
		parsed.Opaque != "" || parsed.RawPath != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("Multica public origin must be an HTTPS origin")
	}
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value != 443 {
			return nil, errors.New("Multica public origin port is invalid")
		}
	}
	if port == "443" {
		port = ""
	}
	host := hostname
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	return &url.URL{Scheme: "https", Host: host}, nil
}

func parseEndpointID(endpointID string) (string, error) {
	keyID, encoded, ok := strings.Cut(endpointID, "_")
	if !ok || !dispatchKeyIDPattern.MatchString(keyID) || encoded == "" {
		return "", errors.New("agent dispatch endpoint id is invalid")
	}
	random, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(random) != 16 || base64.RawURLEncoding.EncodeToString(random) != encoded {
		return "", errors.New("agent dispatch endpoint id is invalid")
	}
	return keyID, nil
}
