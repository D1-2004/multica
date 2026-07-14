// End-to-end probe: does the AWS S3 SDK, signing with managed (STS) credentials,
// actually round-trip an object through the Aone OSS bucket? Uploads one small
// object, reads it back, deletes it.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/provider"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/urn"
)

type creds struct {
	bucket string
	p      provider.CredentialProvider
}

func (c *creds) Retrieve(_ context.Context) (aws.Credentials, error) {
	cr, err := c.p.GetCredential(urn.OfAliyunOssBucketName(c.bucket))
	if err != nil {
		return aws.Credentials{}, err
	}
	return aws.Credentials{
		AccessKeyID: cr.AccessKeyId, SecretAccessKey: cr.AccessKeySecret,
		SessionToken: cr.SecurityToken, Source: "aone-authz",
		CanExpire: true, Expires: time.Now().Add(10 * time.Minute),
	}, nil
}

func main() {
	ctx := context.Background()
	bucket := os.Getenv("PROBE_BUCKET")
	region := os.Getenv("PROBE_REGION")
	endpoint := os.Getenv("PROBE_ENDPOINT")

	p, err := provider.GetDefaultCredentialProvider()
	if err != nil {
		fmt.Println("credential provider:", err)
		os.Exit(1)
	}
	cp := aws.NewCredentialsCache(&creds{bucket: bucket, p: p})
	got, err := cp.Retrieve(ctx)
	if err != nil {
		fmt.Println("retrieve credential:", err)
		os.Exit(1)
	}
	fmt.Printf("credential: accessKeyId=%.7s… token=%v\n", got.AccessKeyID, got.SessionToken != "")

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region), config.WithCredentialsProvider(cp))
	if err != nil {
		fmt.Println("aws config:", err)
		os.Exit(1)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = os.Getenv("PROBE_PATH_STYLE") == "true"
	})

	key := "probe/multica-oss-probe.txt"
	body := []byte("multica oss probe " + time.Now().Format(time.RFC3339))

	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		Body: bytes.NewReader(body), ContentType: aws.String("text/plain"),
	}); err != nil {
		fmt.Println("PutObject FAILED:", err)
		os.Exit(1)
	}
	fmt.Println("PutObject: ok")

	out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		fmt.Println("GetObject FAILED:", err)
		os.Exit(1)
	}
	read, _ := io.ReadAll(out.Body)
	out.Body.Close()
	if !bytes.Equal(read, body) {
		fmt.Printf("GetObject MISMATCH: %q\n", read)
		os.Exit(1)
	}
	fmt.Printf("GetObject: ok (%d bytes round-tripped)\n", len(read))

	if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
		fmt.Println("DeleteObject FAILED:", err)
		os.Exit(1)
	}
	fmt.Println("DeleteObject: ok")
	fmt.Println("\n=> OSS works with the S3 SDK + managed STS credentials")
}
