package forwarding

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// SignRegistration preserves the timestamp + newline + exact body wire
// contract used by A2A and OAuth registries during rolling deployments.
func SignRegistration(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func VerifyRegistration(secret []byte, timestamp, signature string, body []byte, now time.Time, maxSkew time.Duration) bool {
	ms, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || len(secret) == 0 || maxSkew <= 0 {
		return false
	}
	at := time.UnixMilli(ms)
	if at.Before(now.Add(-maxSkew)) || at.After(now.Add(maxSkew)) {
		return false
	}
	return hmac.Equal([]byte(SignRegistration(secret, timestamp, body)), []byte(signature))
}
