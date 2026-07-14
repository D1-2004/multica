package service

import "crypto/sha256"

// HashAgentDispatchDeliverySecret returns the value persisted for a delivery
// secret. The raw secret is only returned to the message router during binding
// and must never be stored by Multica.
func HashAgentDispatchDeliverySecret(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}
