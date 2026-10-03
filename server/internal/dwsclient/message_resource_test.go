package dwsclient

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const resourceTestMessage = `{"success":true,"result":{"messages":[{
	"openMessageId":"msg-own","openConversationId":"cid-scene","senderOpenDingTalkId":"open-requester","senderId":"open-requester",
	"content":"[文件] stolen.txt fileId: STOLEN-FROM-TEXT https://alidocs.dingtalk.com/i/nodes/STOLEN-LINK",
	"text":"[图片消息](mediaId=$TEXT-MEDIA)",
	"resources":[
		{"resourceId":"FILE-OWN","resourceIdType":"fileId","resourceType":"file","url":"https://alidocs.dingtalk.com/i/nodes/FILE-OWN"},
		{"resourceId":"@IMAGE-OWN","resourceIdType":"mediaId","resourceType":"image","url":"http://oss.example/x.png?x-oss-signature=SECRET-SIG"}
	],
	"resourceRefs":[{"type":"fileId","resourceId":"DERIVED-REF"}],
	"quotedMessage":{"openMessageId":"msg-quoted","openConversationId":"cid-scene","senderOpenDingTalkId":"open-other",
		"resources":[{"resourceId":"QUOTED-FILE","resourceIdType":"fileId","resourceType":"file"}]}
}]}}`

// Only the provider's structured resources of the exact message are its own:
// text notation, links, derived resourceRefs and the quoted message's
// resources never become the current message's attachments.
func TestEmployeeResourceDWSReadsOnlyStructuredOwnResources(t *testing.T) {
	f, cli, dir := startSDK(t, func(tool string, _ map[string]any) string {
		if tool != "list_messages_by_ids" {
			t.Fatalf("unexpected tool %s", tool)
		}
		return resourceTestMessage
	})
	got, err := cli.ReadMessageResources(context.Background(), dir, "cid-scene", "msg-own")
	if err != nil {
		t.Fatal(err)
	}
	if got.MessageID != "msg-own" || got.ConversationID != "cid-scene" || got.SenderOpenDingTalkID != "open-requester" {
		t.Fatalf("identity = %+v", got)
	}
	want := []MessageResource{{ID: "FILE-OWN", IDType: "fileId", Type: "file"}, {ID: "@IMAGE-OWN", IDType: "mediaId", Type: "image"}}
	if len(got.Resources) != len(want) || got.Resources[0] != want[0] || got.Resources[1] != want[1] {
		t.Fatalf("resources = %+v", got.Resources)
	}
	if got.Quoted == nil || got.Quoted.MessageID != "msg-quoted" || got.Quoted.ConversationID != "cid-scene" || got.Quoted.SenderOpenDingTalkID != "open-other" {
		t.Fatalf("quoted = %+v", got.Quoted)
	}
	calls := f.toolCalls("list_messages_by_ids")
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	if ids, _ := calls[0].Args["openMsgIds"].([]any); len(ids) != 1 || ids[0] != "msg-own" {
		t.Fatalf("args = %+v", calls[0].Args)
	}
}

func TestEmployeeResourceDWSRejectsUnverifiableMessages(t *testing.T) {
	for name, tc := range map[string]struct {
		raw, conversation, message string
	}{
		"other message":       {resourceTestMessage, "cid-scene", "msg-other"},
		"other conversation":  {resourceTestMessage, "cid-other", "msg-own"},
		"two messages":        {`{"success":true,"result":{"messages":[{"openMessageId":"m","openConversationId":"c","senderOpenDingTalkId":"s"},{"openMessageId":"m","openConversationId":"c","senderOpenDingTalkId":"s"}]}}`, "c", "m"},
		"failed":              {`{"success":false,"result":{"messages":[{"openMessageId":"m","openConversationId":"c","senderOpenDingTalkId":"s"}]}}`, "c", "m"},
		"sender disagrees":    {`{"success":true,"result":{"messages":[{"openMessageId":"m","openConversationId":"c","senderOpenDingTalkId":"s","senderId":"x"}]}}`, "c", "m"},
		"missing sender":      {`{"success":true,"result":{"messages":[{"openMessageId":"m","openConversationId":"c"}]}}`, "c", "m"},
		"ambiguous id":        {`{"success":true,"result":{"messages":[{"openMessageId":"m","openConversationId":"c","senderOpenDingTalkId":"s","resources":[{"resourceId":"r","resourceIdType":"fileId","resourceType":"file"},{"resourceId":"r","resourceIdType":"mediaId","resourceType":"image"}]}]}}`, "c", "m"},
		"bad id type":         {`{"success":true,"result":{"messages":[{"openMessageId":"m","openConversationId":"c","senderOpenDingTalkId":"s","resources":[{"resourceId":"r","resourceIdType":"url","resourceType":"file"}]}]}}`, "c", "m"},
		"traversal id":        {`{"success":true,"result":{"messages":[{"openMessageId":"m","openConversationId":"c","senderOpenDingTalkId":"s","resources":[{"resourceId":"../etc/passwd x","resourceIdType":"fileId","resourceType":"file"}]}]}}`, "c", "m"},
		"cross-scene quote":   {`{"success":true,"result":{"messages":[{"openMessageId":"m","openConversationId":"c","senderOpenDingTalkId":"s","quotedMessage":{"openMessageId":"q","openConversationId":"other"}}]}}`, "c", "m"},
		"not json":            {`not json`, "c", "m"},
		"oversized response":  {`{"success":true,"pad":"` + strings.Repeat("x", MaxResponseBytes) + `"}`, "c", "m"},
		"incomplete identity": {resourceTestMessage, "", "msg-own"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMessageResources([]byte(tc.raw), tc.conversation, tc.message); err == nil {
				t.Fatal("unverifiable message accepted")
			}
		})
	}
	// A quote that omits its conversation stays in the enclosing one.
	got, err := ParseMessageResources([]byte(`{"success":true,"result":{"messages":[{"openMessageId":"m","openConversationId":"c","senderOpenDingTalkId":"s","quotedMessage":{"openMessageId":"q"}}]}}`), "c", "m")
	if err != nil || got.Quoted == nil || got.Quoted.ConversationID != "c" || len(got.Resources) != 0 {
		t.Fatalf("quoted = %+v %v", got.Quoted, err)
	}
}

func startResourceDownload(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	messageFileHTTPClient = func() *http.Client { return srv.Client() }
	t.Cleanup(func() { messageFileHTTPClient = defaultMessageFileHTTPClient })
	return srv, &hits
}

// The signed URL is used only inside the trusted client: it never reaches the
// caller, an error string or a redirect target on another host.
func TestEmployeeResourceDWSDownloadKeepsSignedURLInside(t *testing.T) {
	content := []byte("RANDOM-CODE-4711\n")
	var gotHeader atomic.Value
	srv, hits := startResourceDownload(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("x-oss-signature") != "SECRET-SIG" {
			http.Error(w, "unsigned", http.StatusForbidden)
			return
		}
		gotHeader.Store(r.Header.Get("X-Provider-Auth"))
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(content)
	})
	signed := srv.URL + "/yundisk/file?x-oss-signature=SECRET-SIG"
	f, cli, dir := startSDK(t, func(tool string, args map[string]any) string {
		if tool != "download_file" || args["fileId"] != "FILE-OWN" {
			t.Fatalf("unexpected %s %v", tool, args)
		}
		return `{"success":true,"result":{"downloadUrl":"` + signed + `","fileName":"notes.txt","fileSize":17,"headers":{"X-Provider-Auth":"provider-token"}}}`
	})
	file, err := cli.DownloadMessageFile(context.Background(), dir, "FILE-OWN", 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(file.Data, content) || file.Name != "notes.txt" || file.DeclaredSize != 17 || file.ContentType != "text/plain" {
		t.Fatalf("file = %+v", file)
	}
	if gotHeader.Load() != "provider-token" || hits.Load() != 1 || len(f.toolCalls("download_file")) != 1 {
		t.Fatalf("header=%v hits=%d", gotHeader.Load(), hits.Load())
	}
}

func TestEmployeeResourceDWSDownloadLimitsAndFailures(t *testing.T) {
	big := bytes.Repeat([]byte("a"), 64)
	for name, tc := range map[string]struct {
		declared string
		url      func(srv string) string
		handler  http.HandlerFunc
		want     error
		hits     int32
	}{
		"declared oversize is not fetched": {declared: `,"fileSize":65`, url: func(s string) string { return s + "/f?sig=SECRET-SIG" }, want: ErrMessageFileTooLarge, hits: 0},
		"undeclared oversize body": {url: func(s string) string { return s + "/f?sig=SECRET-SIG" }, handler: func(w http.ResponseWriter, _ *http.Request) {
			w.(http.Flusher).Flush()
			_, _ = w.Write(append(big, 'x'))
		}, want: ErrMessageFileTooLarge, hits: 1},
		"content-length oversize": {url: func(s string) string { return s + "/f?sig=SECRET-SIG" }, handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "65")
			_, _ = w.Write(append(big, 'x'))
		}, want: ErrMessageFileTooLarge, hits: 1},
		"plain http":   {url: func(s string) string { return strings.Replace(s, "https://", "http://", 1) + "/f?sig=SECRET-SIG" }, want: ErrMessageFileUnavailable, hits: 0},
		"userinfo":     {url: func(s string) string { return strings.Replace(s, "https://", "https://user:SECRET-SIG@", 1) + "/f" }, want: ErrMessageFileUnavailable, hits: 0},
		"provider 404": {url: func(s string) string { return s + "/f?sig=SECRET-SIG" }, handler: func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }, want: ErrMessageFileUnavailable, hits: 1},
		"redirect to plain http": {url: func(s string) string { return s + "/f?sig=SECRET-SIG" }, handler: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://127.0.0.1:1/leak?sig=SECRET-SIG", http.StatusFound)
		}, want: ErrMessageFileUnavailable, hits: 1},
	} {
		t.Run(name, func(t *testing.T) {
			handler := tc.handler
			if handler == nil {
				handler = func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(big) }
			}
			srv, hits := startResourceDownload(t, handler)
			_, cli, dir := startSDK(t, func(string, map[string]any) string {
				return `{"success":true,"result":{"downloadUrl":"` + tc.url(srv.URL) + `","fileName":"a.txt"` + tc.declared + `}}`
			})
			_, err := cli.DownloadMessageFile(context.Background(), dir, "FILE-OWN", 64)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "SECRET-SIG") || strings.Contains(err.Error(), srv.URL) {
				t.Fatalf("error leaks the signed URL: %v", err)
			}
			if hits.Load() != tc.hits {
				t.Fatalf("download hits = %d, want %d", hits.Load(), tc.hits)
			}
		})
	}
	// A provider business failure or a response without a URL is unavailable.
	for _, payload := range []string{`{"success":false,"errorCode":"NO_PERMISSION","errorMsg":"denied"}`, `{"success":true,"result":{"fileName":"a.txt"}}`} {
		_, cli, dir := startSDK(t, func(string, map[string]any) string { return payload })
		if _, err := cli.DownloadMessageFile(context.Background(), dir, "FILE-OWN", 64); !errors.Is(err, ErrMessageFileUnavailable) {
			t.Fatalf("%s: err = %v", payload, err)
		}
	}
	if _, err := (CLI{}).DownloadMessageFile(context.Background(), t.TempDir(), "bad id", 64); err == nil {
		t.Fatal("invalid file id accepted")
	}
}
