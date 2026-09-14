package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type dshUploadTransport func(*http.Request) (*http.Response, error)

func (f dshUploadTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const nativeUploadFixture = `{"type":"multica/task-trajectory","version":1,"sessionId":"session","requestId":"mine","firstSeq":10,"lastSeq":12}
{"type":"session","version":3,"id":"session","createdAt":1,"isSeeded":false}
{"type":"turn/start","seq":10,"time":2,"data":{"turn":9}}
{"type":"user/message","seq":11,"time":3,"data":{"source":{"kind":"user","rpcId":"mine"}}}
{"type":"turn/end","seq":12,"time":4,"data":{"turn":9,"reason":{"kind":"completed"}}}
`

func TestDSHNativeTrajectoryUploadUsesOnlyTaskIdentityAndRequiresReceipt(t *testing.T) {
	for _, status := range []int{204, 302, 403, 409, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			client := &Client{baseURL: "https://pre.example", token: "daemon-token-must-not-be-used", client: &http.Client{Transport: dshUploadTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "PUT" || r.URL.String() != "https://pre.example/api/tasks/task/dsh-trajectory" || r.Header.Get("Authorization") != "Bearer mat_fixture" || r.Header.Get("X-Workspace-ID") != "workspace" {
					t.Fatal("wrong upload identity or route")
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != nativeUploadFixture || r.Header.Get("X-Content-SHA256") != fmt.Sprintf("%x", sha256.Sum256(body)) {
					t.Fatal("trajectory bytes or digest changed")
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://another.example/should-not-receive-artifact"}}, Body: io.NopCloser(strings.NewReader("ignored")), Request: r}, nil
			})}}
			err := client.uploadDSHTrajectory(context.Background(), "task", "workspace", "mat_fixture", []byte(nativeUploadFixture))
			if (err == nil) != (status == 204) || calls != 1 {
				t.Fatalf("upload status=%d calls=%d error=%v", status, calls, err)
			}
		})
	}
}

func TestDSHNativeTrajectoryUploadRejectsDaemonTokenBeforeNetwork(t *testing.T) {
	c := &Client{}
	if c.uploadDSHTrajectory(context.Background(), "task", "workspace", "daemon-token", []byte(nativeUploadFixture)) == nil {
		t.Fatal("daemon credential accepted")
	}
}
