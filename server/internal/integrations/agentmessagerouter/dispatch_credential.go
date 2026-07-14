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
	"strconv"
	"strings"
)

const dispatchDomain = "multica-agent-dispatch:v1:"

var dispatchKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,31}$`)

type DispatchKeyring struct {
	currentKeyID string
	keys         map[string][]byte
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
		return "", err
	}
	if k == nil {
		return "", errors.New("agent dispatch keyring is not configured")
	}
	key, ok := k.keys[keyID]
	if !ok {
		return "", errors.New("agent dispatch endpoint uses an unknown key id")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(dispatchDomain + endpointID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
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
