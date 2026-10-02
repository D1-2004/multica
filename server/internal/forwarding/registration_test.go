package forwarding

import (
	"testing"
	"time"
)

func TestRegistrationSignatureBindsBodyAndTime(t *testing.T) {
	secret := []byte("secret")
	body := []byte(`{"target":"pre"}`)
	now := time.UnixMilli(1234567890000)
	sig := SignRegistration(secret, "1234567890000", body)
	if !VerifyRegistration(secret, "1234567890000", sig, body, now, time.Minute) {
		t.Fatal("valid signature refused")
	}
	if VerifyRegistration(secret, "1234567890000", sig, []byte(`{"target":"prod"}`), now, time.Minute) {
		t.Fatal("changed body accepted")
	}
	if VerifyRegistration(secret, "1234567890000", sig, body, now.Add(2*time.Minute), time.Minute) {
		t.Fatal("expired signature accepted")
	}
	if VerifyRegistration(secret, "9223372036854775807", sig, body, now, time.Minute) {
		t.Fatal("overflow timestamp accepted")
	}
}
