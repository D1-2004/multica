package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/helper"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/provider"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/urn"
)

const redisAuthzInstanceIDEnv = "REDIS_AUTHZ_INSTANCE_ID"

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

	endpoint, err := helper.GetEndpoint(resourceURN)
	if err != nil {
		return nil, "", fmt.Errorf("get Aone Redis endpoint for %s: %w", instanceID, err)
	}
	addr, err := normalizeRedisEndpoint(endpoint)
	if err != nil {
		return nil, "", fmt.Errorf("normalize Aone Redis endpoint for %s: %w", instanceID, err)
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

	if _, _, err := net.SplitHostPort(raw); err == nil {
		return raw, nil
	}
	if strings.Contains(raw, ":") {
		return "", fmt.Errorf("endpoint %q has an invalid port", endpoint)
	}
	return net.JoinHostPort(raw, "6379"), nil
}
