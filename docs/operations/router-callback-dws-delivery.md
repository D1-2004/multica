# Router callback reply delivery

Router retains reply selection (outbound mode, shouldReply, original sender, conversation, mentions and content) and freezes a credential-free `dwsDelivery` in its execution report or handoff. It returns the same plan on replay. Router sends only fixed control acknowledgments using TextContent. Managed response actions retain their existing owner; Router returns no additional delivery for them.

Multica freezes the returned plan in the existing callback outbox, issues a short-lived DWS context for its original UID and organization, and sends from an isolated configuration directory to the original environment. Ordinary text and Markdown use the DWS default content path with AI tagging disabled. Acceptance is persisted before polling the send task. Completion requires a successful receipt with a message ID and the expected conversation. A worker restart queries the stored send task rather than sending again. Ambiguous sends reuse the same idempotency key for at most 23 hours. If DWS explicitly reports a duplicate UUID without returning the original receipt, the worker persists `confirmation_unavailable`, stops sending and reports `send_confirmation_unavailable`. A duplicate is not proof of delivery, and a new key must never be minted to bypass it. Original receipt recovery is not supported by the current provider API; these records require investigation. This does not change prompts or Agent execution.

Deploy Multica and migration 9222 to all pre-release replicas first, then Router. An old Router response without dwsDelivery means Router owned the send. Old reports without a frozen plan must not be reconstructed or resent. Rollback requires reverting Router first and draining new delivery plans before reverting Multica; keep the additive columns while any new replica remains.

Verify both deploy orders and target hosts, then a real pre-release message: Router execution report must contain its frozen plan, Multica outbox must contain a delivered receipt, and the original sender/conversation/content must match the visible DWS message. A successful callback or accepted send task alone is insufficient.

### Message command diagnostics

A failed DWS CLI send or status lookup preserves structured `server_error_code`,
`trace_id`, `category`, and `reason` identifiers from stdout or stderr. The worker
persists these identifiers in its existing retry error and log entry. Raw error
messages, suggested commands, credentials, and message content are omitted.
This changes diagnostics only; it does not resend existing records or change the
retry policy, reply selection, sender identity, or message body.

### Sender-scoped reply targets

The frozen plan includes `sourceOpenMessageId` from the trusted inbound message.
Before sending, Multica reads exactly that message through DWS under the actual
sending account, verifies both the message ID and conversation ID, and uses the
returned sender identity for the direct recipient or group mention. The body
retains its existing mention placeholder and formatting. Router rejects new
mention-bearing plans without a source reference. Missing, conflicting or
cross-conversation reads fail before sending; names are never an identity fallback.
