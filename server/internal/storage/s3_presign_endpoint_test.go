package storage

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
)

func TestOSSPresignedTransfersUseBucketHostname(t *testing.T) {
	t.Setenv("S3_BUCKET", "test-bucket")
	t.Setenv("S3_REGION", "cn-beijing")
	t.Setenv("AWS_ENDPOINT_URL", "https://oss-cn-beijing-internal.aliyuncs.com")
	t.Setenv("AWS_PRESIGN_ENDPOINT_URL", "")
	t.Setenv("S3_USE_PATH_STYLE", "")
	t.Setenv("CLOUDFRONT_DOMAIN", "")
	store := NewS3StorageFromEnv(WithCredentialsProvider(credentials.NewStaticCredentialsProvider("TEST", "SECRET", "SESSION")))
	if store == nil {
		t.Fatal("storage unavailable")
	}
	key := "dsh-plugin-builds/workspace/build/intent/tree.tgz"
	for _, method := range []string{"GET", "PUT"} {
		t.Run(method, func(t *testing.T) {
			var signed string
			var err error
			if method == "GET" {
				signed, err = store.PresignGet(context.Background(), key, time.Minute)
			} else {
				signed, err = store.PresignPut(context.Background(), key, "application/gzip", time.Minute)
			}
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(signed)
			if err != nil || u.Host != "test-bucket.oss-cn-beijing.aliyuncs.com" || u.Path != "/"+key {
				t.Fatal("OSS transfer must address the exact object through the public bucket hostname")
			}
			if u.Query().Get("X-Amz-Security-Token") != "SESSION" || u.Query().Get("X-Amz-Expires") != "60" {
				t.Fatal("temporary credentials or expiration missing")
			}
		})
	}
	// Existing stored path-style URLs must remain readable after the default changes.
	for _, raw := range []string{
		"https://oss-cn-beijing-internal.aliyuncs.com/test-bucket/" + key,
		"https://test-bucket.oss-cn-beijing-internal.aliyuncs.com/" + key,
	} {
		if store.KeyFromURL(raw) != key {
			t.Fatal("existing object identity changed")
		}
	}
}

func TestOSSAddressingDefaultDoesNotMatchOtherStores(t *testing.T) {
	t.Setenv("S3_USE_PATH_STYLE", "")
	for _, endpoint := range []string{
		"https://oss-cn-beijing.aliyuncs.com", "https://oss-cn-beijing-internal.aliyuncs.com/",
		"https://minio.example.com", "https://oss-cn-beijing.aliyuncs.com.evil.example",
	} {
		want := !strings.HasSuffix(strings.TrimSuffix(endpoint, "/"), ".aliyuncs.com")
		if got := s3UsePathStyleFromEnv(endpoint); got != want {
			t.Errorf("endpoint %s: path style %v, want %v", endpoint, got, want)
		}
	}
	t.Setenv("S3_USE_PATH_STYLE", "true")
	if !s3UsePathStyleFromEnv("https://oss-cn-beijing.aliyuncs.com") {
		t.Fatal("explicit addressing override ignored")
	}
}

// A presigned URL is always handed to something outside the server's network,
// so it has to be signed against a host that thing can reach. Pre-release's own
// endpoint is VPC-internal, and a URL signed against it resolves nowhere.
func TestPublicEndpointForStripsTheInternalSuffix(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "aliyun oss internal endpoint",
			in:   "https://oss-cn-zhangjiakou-internal.aliyuncs.com",
			want: "https://oss-cn-zhangjiakou.aliyuncs.com",
		},
		{
			name: "trailing slash is ignored",
			in:   "https://oss-cn-hangzhou-internal.aliyuncs.com/",
			want: "https://oss-cn-hangzhou.aliyuncs.com",
		},
		{
			// Already public: no second client, no behaviour change.
			name: "public endpoint needs no rewrite",
			in:   "https://oss-cn-zhangjiakou.aliyuncs.com",
			want: "",
		},
		{
			name: "unset endpoint",
			in:   "",
			want: "",
		},
		{
			// A host that merely contains the word must not be rewritten; only
			// the region-suffix form is Aliyun's internal convention.
			name: "unrelated host is left alone",
			in:   "https://internal-minio.example.com",
			want: "",
		},
		{
			name: "http is handled too",
			in:   "http://oss-cn-beijing-internal.aliyuncs.com",
			want: "http://oss-cn-beijing.aliyuncs.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := publicEndpointFor(tc.in); got != tc.want {
				t.Errorf("publicEndpointFor(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
