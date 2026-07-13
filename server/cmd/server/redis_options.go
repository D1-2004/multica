package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/provider"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/urn"
)

const (
	redisAuthzInstanceIDEnv = "REDIS_AUTHZ_INSTANCE_ID"
	redisAuthzEndpointEnv   = "REDIS_AUTHZ_ENDPOINT"
)

func redisOptionsFromEnv() (*redis.Options, string, error) {
	if rawURL := strings.TrimSpace(os.Getenv("REDIS_URL")); rawURL != "" {
		opts, err := redis.ParseURL(rawURL)
		if err != nil {
			return nil, "", fmt.Errorf("parse REDIS_URL: %w", err)
		}
		return opts, "url", nil
	}

	instanceID := strings.TrimSpace(os.Getenv(redisAuthzInstanceIDEnv))
	if instanceID == "" {
		return nil, "", nil
	}
	endpoint := strings.TrimSpace(os.Getenv(redisAuthzEndpointEnv))
	if endpoint == "" {
		return nil, "", fmt.Errorf("%s is required when %s is set", redisAuthzEndpointEnv, redisAuthzInstanceIDEnv)
	}
	addr, err := normalizeRedisEndpoint(endpoint)
	if err != nil {
		return nil, "", fmt.Errorf("normalize Aone Redis endpoint for %s: %w", instanceID, err)
	}

	resourceURN := urn.OfAliyunKvStoreInstanceId(instanceID)
	credentialProvider, err := provider.GetDefaultCredentialProvider()
	if err != nil {
		return nil, "", fmt.Errorf("initialize Aone Redis credential provider: %w", err)
	}
	credential, err := credentialProvider.GetCredential(resourceURN)
	if err != nil {
		return nil, "", fmt.Errorf("get Aone Redis credential for %s: %w", instanceID, err)
	}
	if credential.Username == "" || credential.Password == "" {
		return nil, "", fmt.Errorf("Aone Redis credential for %s is incomplete", instanceID)
	}

	return &redis.Options{
		Addr:     addr,
		Password: credential.Username + ":" + credential.Password,
		DB:       0,
	}, "aone-authz", nil
}

func normalizeRedisEndpoint(endpoint string) (string, error) {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return "", fmt.Errorf("endpoint is empty")
	}

	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return "", fmt.Errorf("invalid endpoint %q", endpoint)
		}
		raw = parsed.Host
	}

	if host, port, err := net.SplitHostPort(raw); err == nil {
		// SplitHostPort only splits on the last colon — it accepts any port
		// text, numeric or not. Validate here, where a typo in the configured
		// endpoint can still be reported against the endpoint itself; left to
		// the client it would surface much later as an opaque dial failure,
		// and Redis is load-bearing enough that the server exits on it.
		if host == "" {
			return "", fmt.Errorf("endpoint %q has no host", endpoint)
		}
		n, perr := strconv.ParseUint(port, 10, 16)
		if perr != nil || n == 0 {
			return "", fmt.Errorf("endpoint %q has an invalid port %q", endpoint, port)
		}
		return raw, nil
	}
	if strings.Contains(raw, ":") {
		return "", fmt.Errorf("endpoint %q has an invalid port", endpoint)
	}
	return net.JoinHostPort(raw, "6379"), nil
}
