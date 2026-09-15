package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/sts"
	"github.com/aws/aws-sdk-go-v2/aws"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/helper"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/urn"
)

var ossBucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
var ossRelatedRole = regexp.MustCompile(`^acs:ram::[0-9]+:role/[a-zA-Z0-9@_.-]+$`)

// The resource binding already defines which role this application may assume.
// Never substitute a personal key or invent a role when that binding is absent.
func ossPresignCredentials(base aws.CredentialsProvider) aws.CredentialsProvider {
	return aws.NewCredentialsCache(&ossPresignCredentialsProvider{
		bucket: strings.TrimSpace(os.Getenv(ossAuthzBucketEnv)), base: base,
		role: helper.GetRelatedRoleArn, assume: assumeOSSRole,
	}, func(o *aws.CredentialsCacheOptions) { o.ExpiryWindow = time.Minute })
}

type ossPresignCredentialsProvider struct {
	bucket string
	base   aws.CredentialsProvider
	role   func(string) (string, error)
	assume func(context.Context, aws.Credentials, string, string) (aws.Credentials, error)
}

func (p *ossPresignCredentialsProvider) Retrieve(ctx context.Context) (aws.Credentials, error) {
	if err := ctx.Err(); err != nil {
		return aws.Credentials{}, err
	}
	if !ossBucketName.MatchString(p.bucket) {
		return aws.Credentials{}, fmt.Errorf("invalid OSS authorization bucket")
	}
	role, err := p.role(urn.OfAliyunOssBucketName(p.bucket))
	if err != nil || !ossRelatedRole.MatchString(role) {
		slog.Warn("OSS external credentials unavailable", "stage", "role_binding")
		return aws.Credentials{}, fmt.Errorf("OSS resource-bound role unavailable")
	}
	base, err := p.base.Retrieve(ctx)
	if err != nil || base.AccessKeyID == "" || base.SecretAccessKey == "" {
		slog.Warn("OSS external credentials unavailable", "stage", "managed_key")
		return aws.Credentials{}, fmt.Errorf("OSS managed credential unavailable")
	}
	credential, err := p.assume(ctx, base, role, p.bucket)
	if err != nil {
		slog.Warn("OSS external credentials unavailable", "stage", "assume_role")
		return aws.Credentials{}, fmt.Errorf("OSS bound role assumption failed")
	}
	if credential.AccessKeyID == "" || credential.SecretAccessKey == "" || credential.SessionToken == "" || !credential.CanExpire || time.Until(credential.Expires) < 2*time.Minute {
		return aws.Credentials{}, fmt.Errorf("OSS STS credential incomplete or expiring")
	}
	credential.Source = "aone-authz-sts"
	return credential, nil
}

// The session policy can only reduce the existing role's permissions. The
// worker receives object-specific signatures, never this credential or role.
func ossTransferPolicy(bucket string) string {
	policy := map[string]any{"Version": "1", "Statement": []any{map[string]any{
		"Effect": "Allow", "Action": []string{"oss:GetObject", "oss:PutObject"},
		"Resource": []string{"acs:oss:*:*:" + bucket + "/*"},
	}}}
	raw, _ := json.Marshal(policy)
	return string(raw)
}

type ossSTSTransport struct{ ctx context.Context }

func (t ossSTSTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return http.DefaultTransport.RoundTrip(request.Clone(t.ctx))
}

func assumeOSSRole(ctx context.Context, base aws.Credentials, role, bucket string) (aws.Credentials, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client, err := sts.NewClientWithStsToken(os.Getenv("S3_REGION"), base.AccessKeyID, base.SecretAccessKey, base.SessionToken)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("initialize OSS STS client")
	}
	client.SetTransport(ossSTSTransport{ctx: ctx})
	request := sts.CreateAssumeRoleRequest()
	request.Scheme = "https"
	request.Domain = "sts.aliyuncs.com"
	request.RoleArn, request.RoleSessionName = role, "multica-oss-transfer"
	request.DurationSeconds = requests.NewInteger(3600)
	request.Policy = ossTransferPolicy(bucket)
	request.SetConnectTimeout(5 * time.Second)
	request.SetReadTimeout(15 * time.Second)
	response, err := client.AssumeRole(request)
	if err != nil {
		// SDK errors may contain the signed request. Only a bounded service code
		// is useful for operations; never log the error string or response body.
		code := "unavailable"
		if value, ok := err.(interface{ ErrorCode() string }); ok {
			if candidate := value.ErrorCode(); regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,100}$`).MatchString(candidate) {
				code = candidate
			}
		}
		slog.Warn("OSS STS request failed", "code", code)
		return aws.Credentials{}, fmt.Errorf("OSS STS request failed")
	}
	if response == nil {
		return aws.Credentials{}, fmt.Errorf("OSS STS response absent")
	}
	expires, err := time.Parse(time.RFC3339, response.Credentials.Expiration)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("OSS STS expiration invalid")
	}
	return aws.Credentials{AccessKeyID: response.Credentials.AccessKeyId, SecretAccessKey: response.Credentials.AccessKeySecret,
		SessionToken: response.Credentials.SecurityToken, CanExpire: true, Expires: expires}, nil
}
