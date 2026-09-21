package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

const nativePromptFixture = `{"requestId":"91675c65-a8e3-4b25-85fc-5e82081af8af","sessionId":"session-31e58f19-8669-42f3-98f6-01bc71aa8ad0","mode":"steer","content":[{"type":"text","text":"请看附件"},{"type":"image","mediaType":"image/png","data":"aGVsbG8=","name":"截图.png"},{"type":"file","receiptId":"opaque-upload-receipt"}],"clientTimeZone":"Asia/Shanghai"}`

func TestDSHNativePromptPreservesContentAndDetaches(t *testing.T) {
	p, err := DecodeDSHNativePrompt([]byte(nativePromptFixture))
	if err != nil {
		t.Fatal(err)
	}
	clone, err := p.Clone()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(clone)
	if string(raw) != nativePromptFixture {
		t.Fatal("native input changed during round trip")
	}
	*p.Content[1].Data = "changed"
	*p.ClientTimeZone = "UTC"
	p.Content[0].Type = "file"
	if *clone.Content[1].Data != "aGVsbG8=" || *clone.ClientTimeZone != "Asia/Shanghai" || clone.Content[0].Type != "text" {
		t.Fatal("mutable caller input leaked into clone")
	}
	if clone.DisplayText() != "请看附件\n[DSH image]\n[DSH file]" {
		t.Fatal("incorrect transcript summary")
	}
}

func TestDSHNativePromptRejectsLossyOrAmbiguousInput(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown root":   strings.Replace(nativePromptFixture, `"mode":`, `"model":"foreign","mode":`, 1),
		"unknown part":   strings.Replace(nativePromptFixture, `"type":"text"`, `"type":"text","extra":1`, 1),
		"cross union":    strings.Replace(nativePromptFixture, `"type":"text"`, `"type":"text","name":"x"`, 1),
		"null union":     strings.Replace(nativePromptFixture, `"type":"text"`, `"type":"text","name":null`, 1),
		"null zone":      strings.Replace(nativePromptFixture, `"Asia/Shanghai"`, `null`, 1),
		"bad mode":       strings.Replace(nativePromptFixture, `"steer"`, `"queue-later"`, 1),
		"bad image":      strings.Replace(nativePromptFixture, `"aGVsbG8="`, `"not base64"`, 1),
		"bad MIME":       strings.Replace(nativePromptFixture, `"image/png"`, `"text/plain"`, 1),
		"blank receipt":  strings.Replace(nativePromptFixture, `"opaque-upload-receipt"`, `" "`, 1),
		"unsafe session": strings.Replace(nativePromptFixture, `"session-31e58f19-8669-42f3-98f6-01bc71aa8ad0"`, `"../other"`, 1),
		"unknown zone":   strings.Replace(nativePromptFixture, `"Asia/Shanghai"`, `"Bad/Location"`, 1),
		"second value":   nativePromptFixture + `{}`,
		"oversize":       strings.Replace(nativePromptFixture, "请看附件", strings.Repeat("x", DSHNativePromptMaxBytes), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeDSHNativePrompt([]byte(raw)); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

func TestDSHNativePromptRequiresMeaningfulContent(t *testing.T) {
	p, _ := DecodeDSHNativePrompt([]byte(nativePromptFixture))
	blank := " \n"
	p.Content = []DSHNativePromptPart{{Type: "text", Text: &blank}}
	if p.Validate() == nil {
		t.Fatal("blank-only prompt accepted")
	}
	p.Content = []DSHNativePromptPart{{Type: "file", ReceiptID: &blank}}
	if p.Validate() == nil {
		t.Fatal("blank receipt accepted")
	}
	receipt := "receipt"
	p.Content[0].ReceiptID = &receipt
	if p.Validate() != nil {
		t.Fatal("attachment-only prompt rejected")
	}
}

func TestDSHNativePersistentWorkdir(t *testing.T) {
	for _, value := range []string{"/mnt/multica", "/mnt/multica/files", "/mnt/multica/files/项目"} {
		if !ValidDSHWorkdir(value) {
			t.Fatalf("rejected %q", value)
		}
	}
	for _, value := range []string{"", "/tmp", "/mnt/multica-other", "/mnt/multica/files/..", "/mnt/multica/files/", "/mnt/multica/\x00"} {
		if ValidDSHWorkdir(value) {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestDSHNativePluginRequestIdentity(t *testing.T) {
 sid := "session-71ff28eb-365a-4119-8d44-594a748770e3"
 original := "weixin-abc123"
 first, err := DSHNativeRequestIdentity(sid, original)
 if err != nil { t.Fatal(err) }
 retry, _ := DSHNativeRequestIdentity(sid, original)
 other, _ := DSHNativeRequestIdentity("session-c62a4a70-13bf-485a-9559-179e167c4765", original)
 if first != retry || first == other { t.Fatal("native requests must deduplicate within their exact session") }
 browser := "cd9f370c-9dbe-4968-bfc3-b93e461c8e69"
 kept, err := DSHNativeRequestIdentity(sid, browser)
 if err != nil || kept.String() != browser { t.Fatal("existing browser identity changed") }
 for _, id := range []string{"", "bad request", "../bad", "00000000-0000-0000-0000-000000000000"} {
  if _, err := DSHNativeRequestIdentity(sid, id); err == nil { t.Fatalf("accepted invalid identity %q", id) }
 }
}
