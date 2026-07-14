package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/provider"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/urn"
)

// ossAuthzBucketEnv names the bucket whose credentials come from the managed
// credential service rather than from static keys. Same shape as
// REDIS_AUTHZ_INSTANCE_ID (see redis_options.go).
const ossAuthzBucketEnv = "OSS_AUTHZ_BUCKET"

// ossCredentialsFromEnv returns an AWS credentials provider backed by the
// managed credential service, or nil when OSS_AUTHZ_BUCKET is unset (the
// storage layer then falls back to static AWS_ACCESS_KEY_ID / SECRET, or to
// local disk when S3_BUCKET is also unset).
//
// The bucket's policy only admits callers whose acs:AccessId is TMP.* or STS.*,
// so static keys are not merely discouraged here — they are rejected outright.
// The credentials it hands out expire, which is why this returns a provider the
// AWS SDK re-invokes rather than one set of keys read at boot.
func ossCredentialsFromEnv() (aws.CredentialsProvider, error) {
	bucket := strings.TrimSpace(os.Getenv(ossAuthzBucketEnv))
	if bucket == "" {
		return nil, nil
	}
	credentialProvider, err := provider.GetDefaultCredentialProvider()
	if err != nil {
		return nil, fmt.Errorf("initialize OSS credential provider: %w", err)
	}
	// Cache so every S3 call does not re-fetch; the cache refreshes on the
	// expiry the provider reports.
	cache := aws.NewCredentialsCache(&ossCredentialsProvider{
		bucket:   bucket,
		provider: credentialProvider,
	})

	// Probe once at boot. The credential material only exists inside the
	// deployed pod (the managed service authenticates the machine, not a key on
	// disk), so a misconfigured bucket or a missing application binding shows up
	// here rather than as an opaque 403 on the first user upload. Deliberately
	// not fatal: a transient credential-service hiccup must not crash-loop the
	// pod, and the SDK re-fetches on every call anyway.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := cache.Retrieve(ctx); err != nil {
		slog.Error("OSS credentials unavailable — attachment uploads will fail until this resolves; check that the application is bound to the bucket in CloudCenter",
			"bucket", bucket, "error", err)
	} else {
		slog.Info("OSS credentials resolved via the managed credential service", "bucket", bucket)
	}
	return cache, nil
}

// ossCredentialsProvider adapts the managed credential service to the AWS
// SDK's provider interface.
type ossCredentialsProvider struct {
	bucket   string
	provider provider.CredentialProvider
}

func (p *ossCredentialsProvider) Retrieve(_ context.Context) (aws.Credentials, error) {
	credential, err := p.provider.GetCredential(urn.OfAliyunOssBucketName(p.bucket))
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("get OSS credential for %s: %w", p.bucket, err)
	}
	if credential.AccessKeyId == "" || credential.AccessKeySecret == "" {
		return aws.Credentials{}, fmt.Errorf("OSS credential for %s is incomplete", p.bucket)
	}
	return aws.Credentials{
		AccessKeyID:     credential.AccessKeyId,
		SecretAccessKey: credential.AccessKeySecret,
		SessionToken:    credential.SecurityToken,
		Source:          "aone-authz",
		// The service does not report an expiry, but the token it mints is
		// short-lived. Expire our copy well inside that window so the SDK
		// re-fetches rather than signing with a token the bucket has stopped
		// accepting — a stale token surfaces as an opaque 403 on upload.
		CanExpire: true,
		Expires:   time.Now().Add(10 * time.Minute),
	}, nil
}
