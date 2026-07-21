package dingtalk

import "testing"

func TestDingTalkPictureName(t *testing.T) {
	name, contentType, err := dingtalkPictureName("https://files.example.test/path/object.png?token=secret")
	if err != nil {
		t.Fatalf("dingtalkPictureName: %v", err)
	}
	if name != "dingtalk-picture.png" || contentType != "image/png" {
		t.Fatalf("name/contentType = %q/%q", name, contentType)
	}
	if _, _, err := dingtalkPictureName("https://files.example.test/path/object.bin"); err == nil {
		t.Fatal("expected unsupported picture extension to be rejected")
	}
}
