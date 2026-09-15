package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/urn"
)

func TestOSSPresignUsesResourceBoundRoleAndRealExpiry(t *testing.T) {
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	p := &ossPresignCredentialsProvider{
		bucket: "test-bucket", base: credentials.NewStaticCredentialsProvider("LTAI-test", "secret-test", ""),
		role: func(resource string) (string, error) {
			if resource != urn.OfAliyunOssBucketName("test-bucket") {
				t.Fatal("wrong resource binding")
			}
			return "acs:ram::123456:role/bucket-bound", nil
		},
		assume: func(ctx context.Context, base aws.Credentials, role, bucket string) (aws.Credentials, error) {
			if base.AccessKeyID != "LTAI-test" || base.SessionToken != "" || role != "acs:ram::123456:role/bucket-bound" || bucket != "test-bucket" {
				t.Fatal("incorrect assumption inputs")
			}
			return aws.Credentials{AccessKeyID: "STS.test", SecretAccessKey: "test-secret", SessionToken: "test-token", CanExpire: true, Expires: expires}, nil
		},
	}
	got, err := p.Retrieve(context.Background())
	if err != nil || got.AccessKeyID != "STS.test" || got.SessionToken == "" || !got.Expires.Equal(expires) || got.Source != "aone-authz-sts" {
		t.Fatal("did not return the actual temporary credential")
	}
}

func TestOSSPresignNeverFallsBackToManagedKey(t *testing.T) {
	for _, failure := range []string{"role", "base", "assume", "no_token", "no_expiry", "expired", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			p := &ossPresignCredentialsProvider{bucket: "test-bucket",
				base: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					if failure == "base" {
						return aws.Credentials{}, errors.New("test-secret")
					}
					return aws.Credentials{AccessKeyID: "LTAI-test", SecretAccessKey: "test-secret"}, nil
				}),
				role: func(string) (string, error) {
					if failure == "role" {
						return "", errors.New("test-secret")
					}
					return "acs:ram::123456:role/test", nil
				},
				assume: func(context.Context, aws.Credentials, string, string) (aws.Credentials, error) {
					calls++
					if failure == "assume" {
						return aws.Credentials{}, errors.New("test-secret")
					}
					c := aws.Credentials{AccessKeyID: "STS.test", SecretAccessKey: "test-secret", SessionToken: "test-token", CanExpire: true, Expires: time.Now().Add(time.Hour)}
					if failure == "no_token" {
						c.SessionToken = ""
					}
					if failure == "no_expiry" {
						c.CanExpire = false
					}
					if failure == "expired" {
						c.Expires = time.Now()
					}
					return c, nil
				},
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "cancelled" {
				cancel()
			}
			c, err := p.Retrieve(ctx)
			if err == nil || c.AccessKeyID != "" || strings.Contains(err.Error(), "test-secret") {
				t.Fatal("failed closed credential boundary")
			}
			if (failure == "role" || failure == "base" || failure == "cancelled") && calls != 0 {
				t.Fatal("assumed a role after prerequisite failure")
			}
		})
	}
}

func TestOSSTransferPolicyRestrictsExistingRole(t *testing.T) {
	var policy struct {
		Version   string
		Statement []struct {
			Effect           string
			Action, Resource []string
		}
	}
	if err := json.Unmarshal([]byte(ossTransferPolicy("test-bucket")), &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Version != "1" || len(policy.Statement) != 1 {
		t.Fatal("unexpected policy")
	}
	s := policy.Statement[0]
	if s.Effect != "Allow" || strings.Join(s.Action, ",") != "oss:GetObject,oss:PutObject" || strings.Join(s.Resource, ",") != "acs:oss:*:*:test-bucket/*" {
		t.Fatal("transfer policy broadened")
	}
}
