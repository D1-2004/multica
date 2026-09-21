package dwsclient

import "testing"

func TestA2UIReceiptRequiresExplicitSuccess(t *testing.T) {
	good := `{"ok":true,"result":{"success":true,"result":{"bizId":"card","cardInstanceId":42}}}`
	r, e := parseA2UIReceipt([]byte(good))
	if e != nil || r.BizID != "card" {
		t.Fatal(r, e)
	}
	for _, raw := range []string{`{}`, `{"ok":true}`, `{"ok":false,"result":{"success":true,"result":{"bizId":"card","cardInstanceId":42}}}`, `{"ok":true,"result":{"success":false}}`, `{"ok":true,"result":{"success":true,"result":{"bizId":"card"}}}`} {
		if _, e := parseA2UIReceipt([]byte(raw)); e == nil {
			t.Fatalf("unconfirmed send accepted: %s", raw)
		}
	}
}
