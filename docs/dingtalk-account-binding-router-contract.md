# DingTalk Account Binding Router Contract

Multica obtains a short-lived DingTalk account-binding credential from Router:

```
POST /api/account-binding-tokens
Authorization: Bearer <service credential>
{
  "agentId": "<agent UUID>",
  "dispatchPath": "/api/webhooks/agent-dispatch/<endpointId>"
}
```

`dispatchPath` is a path only. Multica must not send a public origin or a full
callback URL in this request.

Router returns the canonical callback URL for its own configured origin:

```
{
  "bindingToken": "<short-lived token>",
  "expiresAt": "<RFC3339 timestamp>",
  "dispatchUrl": "https://<router-configured-origin>/api/webhooks/agent-dispatch/<endpointId>"
}
```

Multica verifies that `dispatchUrl` is HTTPS and has the requested endpoint
path. It writes that canonical URL to the agent dispatch-endpoint record and
the pending DingTalk account binding, then uses it in the QR-code fragment.
Existing records with a historical origin remain readable when their endpoint
path matches; the next successful binding replaces their URL with Router's
canonical value.

## History

- 2026-07-21: Binding-token requests changed from `dispatchUrl` to
  `dispatchPath`; Router now supplies `dispatchUrl`.

## Reason

Router owns its deployment origin and is therefore the only component that can
produce the callback's canonical full URL. Keeping Multica's request to an
endpoint path avoids configuration-origin drift while preserving existing
endpoint identifiers.
