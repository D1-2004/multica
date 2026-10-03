package employeeresource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// Over-limit resources stay visible with an explicit state; the window
// download budget is shared across messages.
func TestEmployeeResourcePlanLimitsAreExplicit(t *testing.T) {
	kinds := []string{"file", "image", "file", "file", "image", "file", "file", "image", "video", "other", "file"}
	verdicts, used := Plan(kinds, DefaultLimits(), 4)
	want := []Verdict{
		{Fetch: true}, {State: Unsupported, Reason: ReasonVisionUnavailable}, {Fetch: true}, {Fetch: true},
		{State: Unsupported, Reason: ReasonVisionUnavailable}, {Fetch: true}, {State: Unavailable, Reason: ReasonTooManyFiles},
		{State: Unavailable, Reason: ReasonTooManyImages}, {State: Unsupported, Reason: ReasonUnsupportedType},
		{State: Unsupported, Reason: ReasonUnsupportedType}, {State: Unavailable, Reason: ReasonTooManyFiles},
	}
	if used != 4 || len(verdicts) != len(want) {
		t.Fatalf("used=%d verdicts=%+v", used, verdicts)
	}
	for i := range want {
		if verdicts[i] != want[i] {
			t.Fatalf("verdict %d (%s) = %+v, want %+v", i, kinds[i], verdicts[i], want[i])
		}
	}
	// A second message in the same window gets only what is left.
	verdicts, used = Plan([]string{"file", "file"}, DefaultLimits(), 1)
	if used != 1 || !verdicts[0].Fetch || verdicts[1].Fetch || verdicts[1].Reason != ReasonTooManyFiles {
		t.Fatalf("window budget: used=%d %+v", used, verdicts)
	}
	if verdicts, used = Plan([]string{"file"}, DefaultLimits(), 0); used != 0 || verdicts[0].Fetch {
		t.Fatalf("exhausted window fetched: %+v", verdicts)
	}
}

// Truncation never splits a character, reports the exact range, and the hash
// covers the whole download rather than the excerpt.
func TestEmployeeResourceUTF8BoundaryTruncation(t *testing.T) {
	text := "码" + strings.Repeat("一二三", 3) + "尾" // every rune is 3 bytes
	data := []byte("\xef\xbb\xbf" + text)
	whole := sha256.Sum256(data)
	for budget := 0; budget <= len(text)+1; budget++ {
		got := Extract("notes.txt", "text/plain; charset=utf-8", data, budget, DefaultLimits())
		if !utf8.ValidString(got.Text) || len(got.Text) > budget || len(got.Text)%3 != 0 {
			t.Fatalf("budget %d: text %q", budget, got.Text)
		}
		if got.SHA256 != hex.EncodeToString(whole[:]) || got.SizeBytes != int64(len(data)) {
			t.Fatalf("budget %d: hash/size %s %d", budget, got.SHA256, got.SizeBytes)
		}
		if got.Range == nil || got.Range.Start != 0 || got.Range.End != len(got.Text) || got.Range.Total != len(text) {
			t.Fatalf("budget %d: range %+v", budget, got.Range)
		}
		switch {
		case budget >= len(text):
			if got.State != Available || got.Reason != "" || got.Text != text {
				t.Fatalf("budget %d: %+v", budget, got)
			}
		case budget < 3:
			if got.State != Partial || got.Reason != ReasonTextBudget || got.Text != "" {
				t.Fatalf("budget %d: %+v", budget, got)
			}
		default:
			if got.State != Partial || got.Reason != ReasonTextTruncated || !strings.HasPrefix(text, got.Text) {
				t.Fatalf("budget %d: %+v", budget, got)
			}
		}
	}
	if n := TruncateUTF8("a码", 2); n != 1 {
		t.Fatalf("TruncateUTF8 = %d", n)
	}
}

func TestEmployeeResourceRejectsMismatchCorruptionAndTraversal(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	limits := DefaultLimits()
	limits.MaxFileBytes = 1 << 10
	for name, tc := range map[string]struct {
		file, declared string
		data           []byte
		state          State
		reason         string
	}{
		"png named txt":        {"a.txt", "", png, Unavailable, ReasonTypeMismatch},
		"pdf named csv":        {"a.csv", "", []byte("%PDF-1.7\n1 0 obj"), Unavailable, ReasonTypeMismatch},
		"zip named json":       {"a.json", "", []byte("PK\x03\x04\x14\x00"), Unavailable, ReasonTypeMismatch},
		"html named txt":       {"a.txt", "", []byte("<html><script>x</script></html>"), Unavailable, ReasonTypeMismatch},
		"declared pdf":         {"a.txt", "application/pdf", []byte("hello"), Unavailable, ReasonTypeMismatch},
		"declared image":       {"a.md", "image/png", []byte("hello"), Unavailable, ReasonTypeMismatch},
		"invalid utf8":         {"a.txt", "", []byte("ok \xff\xfe broken"), Unavailable, ReasonInvalidEncoding},
		"late nul byte":        {"a.txt", "", append([]byte(strings.Repeat("a", 600)), 0, 'b'), Unavailable, ReasonInvalidEncoding},
		"utf16":                {"a.txt", "", []byte("\xff\xfeh\x00i\x00"), Unavailable, ReasonInvalidEncoding},
		"corrupt json":         {"a.json", "application/json", []byte(`{"code": 47`), Unavailable, ReasonCorruptContent},
		"traversal":            {"../a.txt", "", []byte("x"), Unavailable, ReasonInvalidName},
		"directory":            {"dir/a.txt", "", []byte("x"), Unavailable, ReasonInvalidName},
		"windows directory":    {`dir\a.txt`, "", []byte("x"), Unavailable, ReasonInvalidName},
		"nul in name":          {"a\x00.txt", "", []byte("x"), Unavailable, ReasonInvalidName},
		"rtl override":         {"report‮txt.exe", "", []byte("x"), Unavailable, ReasonInvalidName},
		"alternate stream":     {"a.txt:evil", "", []byte("x"), Unavailable, ReasonInvalidName},
		"empty name":           {"", "", []byte("x"), Unavailable, ReasonInvalidName},
		"executable":           {"a.exe", "", []byte("x"), Unsupported, ReasonUnsupportedType},
		"pdf":                  {"a.pdf", "", []byte("%PDF"), Unsupported, ReasonUnsupportedType},
		"double extension":     {"a.txt.sh", "", []byte("x"), Unsupported, ReasonUnsupportedType},
		"oversize":             {"a.txt", "", []byte(strings.Repeat("a", 1025)), Unavailable, ReasonTooLarge},
		"markdown inline html": {"a.md", "text/markdown", []byte("<!-- note -->\n# Title"), Available, ""},
		"octet-stream csv":     {"a.CSV", "application/octet-stream", []byte("a,b\n1,2\n"), Available, ""},
		"valid json":           {"a.json", "", []byte(`{"code":4711}`), Available, ""},
		"empty file":           {"a.txt", "", nil, Available, ""},
	} {
		t.Run(name, func(t *testing.T) {
			got := Extract(tc.file, tc.declared, tc.data, 1<<10, limits)
			if got.State != tc.state || got.Reason != tc.reason {
				t.Fatalf("got %s/%s, want %s/%s", got.State, got.Reason, tc.state, tc.reason)
			}
			if tc.state != Available && got.Text != "" {
				t.Fatalf("rejected file exposed text %q", got.Text)
			}
			if tc.reason == ReasonInvalidName && got.Name != "" {
				t.Fatalf("invalid name kept: %q", got.Name)
			}
		})
	}
}

// The model-facing DTO has no field that could carry a provider ID, URL or
// local path.
func TestEmployeeResourceContextShapeCarriesNoProviderHandles(t *testing.T) {
	raw, err := json.Marshal(Context{Version: ContextVersion, Items: []Item{{Ref: "r", SourceRef: "s", Relation: Current, Kind: "file", State: Available, Range: &Range{}}}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Items []map[string]any `json:"items"`
	}
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for key := range decoded.Items[0] {
		for _, forbidden := range []string{"url", "resource_id", "file_id", "media_id", "path", "header", "token"} {
			if strings.Contains(key, forbidden) {
				t.Fatalf("model-facing field %q", key)
			}
		}
	}
}

// An image file is Deferred for a background vision executor only when its
// bytes really are the named image type; its pixels never become text.
func TestEmployeeResourceExtractImageForBackgroundVision(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01")
	jpeg := []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00")
	limits := DefaultLimits()
	limits.MaxFileBytes = 64
	for name, tc := range map[string]struct {
		file   string
		data   []byte
		ok     bool
		state  State
		reason string
		media  string
	}{
		"png":          {"shot.PNG", png, true, Deferred, ReasonBackgroundVision, "image/png"},
		"jpeg":         {"photo.jpeg", jpeg, true, Deferred, ReasonBackgroundVision, "image/jpeg"},
		"png as jpg":   {"photo.jpg", png, true, Unavailable, ReasonTypeMismatch, ""},
		"text as png":  {"fake.png", []byte("not an image"), true, Unavailable, ReasonTypeMismatch, ""},
		"traversal":    {"../shot.png", png, true, Unavailable, ReasonInvalidName, ""},
		"too large":    {"big.png", append(png, make([]byte, 64)...), true, Unavailable, ReasonTooLarge, ""},
		"not an image": {"notes.txt", []byte("hello"), false, "", "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := ExtractImage(tc.file, tc.data, limits)
			if ok != tc.ok || got.State != tc.state || got.Reason != tc.reason || got.MediaType != tc.media || got.Text != "" {
				t.Fatalf("got %+v ok=%v", got, ok)
			}
			if ok && (got.SHA256 == "" || got.SizeBytes != int64(len(tc.data))) {
				t.Fatalf("hash/size missing: %+v", got)
			}
		})
	}
}
