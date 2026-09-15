package storage

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3ExternalSignatureUsesSTSAndCapsExpiry(t *testing.T) {
	for _, method := range []string{"GET", "PUT"} {
		t.Run(method, func(t *testing.T) {
			internal := credentials.NewStaticCredentialsProvider("LTAI-internal-test", "test-secret", "")
			calls := 0
			store := &S3Storage{bucket: "test-bucket", client: s3.New(s3.Options{Region: "cn-zhangjiakou", BaseEndpoint: aws.String("https://oss-cn-zhangjiakou-internal.aliyuncs.com"), Credentials: internal}),
				presignClient: s3.New(s3.Options{Region: "cn-zhangjiakou", BaseEndpoint: aws.String("https://oss-cn-zhangjiakou.aliyuncs.com"), Credentials: internal}),
				presignCredentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					calls++
					return aws.Credentials{AccessKeyID: "STS.external-test", SecretAccessKey: "test-secret", SessionToken: "test-token", CanExpire: true, Expires: time.Now().Add(5 * time.Minute)}, nil
				}),
			}
			var signed string
			var err error
			if method == "GET" {
				signed, err = store.PresignGet(context.Background(), "source/test.tgz", 30*time.Minute)
			} else {
				signed, err = store.PresignPut(context.Background(), "source/test.tgz", "application/gzip", 30*time.Minute)
			}
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(signed)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			expires, _ := strconv.Atoi(q.Get("X-Amz-Expires"))
			if u.Host != "test-bucket.oss-cn-zhangjiakou.aliyuncs.com" || !strings.HasPrefix(q.Get("X-Amz-Credential"), "STS.external-test/") || q.Get("X-Amz-Security-Token") != "test-token" || expires < 235 || expires > 240 || calls != 1 {
				t.Fatal("wrong external identity, endpoint, expiry or credential snapshot")
			}
			base, err := store.client.Options().Credentials.Retrieve(context.Background())
			if err != nil || base.AccessKeyID != "LTAI-internal-test" || base.SessionToken != "" {
				t.Fatal("internal storage identity changed")
			}
		})
	}
}

func TestS3ExternalSignatureDoesNotFallbackOrLeak(t *testing.T) {
	for _, failure := range []string{"error", "no_token", "expired", "no_expiry"} {
		t.Run(failure, func(t *testing.T) {
			store := &S3Storage{bucket: "test-bucket", client: s3.New(s3.Options{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("LTAI-test", "test-secret", "")}), presignCredentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				if failure == "error" {
					return aws.Credentials{}, errors.New("signed-test-secret")
				}
				c := aws.Credentials{AccessKeyID: "STS.test", SecretAccessKey: "test-secret", SessionToken: "test-token", CanExpire: true, Expires: time.Now().Add(time.Hour)}
				if failure == "no_token" {
					c.SessionToken = ""
				}
				if failure == "expired" {
					c.Expires = time.Now()
				}
				if failure == "no_expiry" {
					c.CanExpire = false
				}
				return c, nil
			})}
			for _, method := range []string{"GET", "PUT"} {
				var u string
				var err error
				if method == "GET" {
					u, err = store.PresignGet(context.Background(), "test", time.Minute)
				} else {
					u, err = store.PresignPut(context.Background(), "test", "application/gzip", time.Minute)
				}
				if err == nil || u != "" || strings.Contains(err.Error(), "test-secret") {
					t.Fatal("issued fallback URL or disclosed provider error")
				}
			}
		})
	}
}
