# GitHub pre-release webhook ingress through production

## Contract

GitHub delivers the pre-release App's events to
`POST /api/webhooks/github/pre` on the public production origin. This route is
an authenticated transport boundary, not a production installation or PR handler.
The existing `POST /api/webhooks/github` continues serving the production App.
Pre-release receives the forwarded request on its existing webhook route and
verifies the original GitHub signature again before any domain handling.

The relay is disabled unless both production runtime keys are non-empty:

| Key | Meaning |
| --- | --- |
| `GITHUB_PRE_WEBHOOK_URL` | Fixed pre-release HTTPS URL ending in `/api/webhooks/github` |
| `GITHUB_PRE_WEBHOOK_SECRET` | Pre-release App's independent GitHub Webhook Secret |

The target URL must have an HTTPS host, port 443 or no explicit port, the
canonical webhook path, and no userinfo, query, or fragment. It cannot use this
deployment's own configured browser/public origin. The destination never comes
from a request header, body, or query parameter. No redirect is followed.

The receiver rejects an invalid signature before forwarding and caps the body
at 10 MiB. It preserves raw bytes, `X-Hub-Signature-256`, `X-GitHub-Event`,
`X-GitHub-Delivery`, and GitHub hook metadata headers. It sends a fixed JSON
content type and a forwarding marker; a marked request cannot enter the relay
again. Cookies, Authorization, forwarding headers, and caller-selected routing
information are never propagated.

One forwarding attempt has an eight-second deadline. The relay returns success
only for a successful pre-release response and never automatically retries.
Failures remain failures for GitHub's delivery history and manual redelivery.
The delivery ID remains unchanged for downstream correlation. All domain
effects and existing idempotency safeguards belong to pre-release; no queue,
installation binding, or PR state is created on production by this route.
Logs contain event/delivery metadata and a safe error class, never bodies,
signatures, Secrets, or upstream response content.

OAuth remains a separate browser flow. The pre-release App retains
`https://pre-fde-workbench.dingtalk.com/api/github/authorize` as its OAuth
callback and `/api/github/setup` as its installation callback. Changing the
Webhook ingress does not move OAuth credentials or workspace bindings to production.

## Rollout and rollback

1. Release the route and runtime whitelist, validate it in pre-release, then
   deploy it to production through its normal release gates.
2. Configure only production with the fixed pre-release URL and the pre-release
   App Secret using the Aone environment trait's JSON-string serialization.
3. Set the `qwen-tag-pre` App Webhook URL to
   `https://fde-workbench.dingtalk.com/api/webhooks/github/pre` and retain SSL
   verification. Leave the production App and both Apps' OAuth settings intact.
4. Redeliver the real GitHub ping. Verify GitHub sees a successful response and
   pre-release is the only environment that handles a subsequent real event.

Removing either relay key disables this ingress after deployment. A rollback
must also adjust the App's Webhook URL; pre-release cannot receive direct GitHub
deliveries in the current network topology. Existing production App bindings
and repository access do not depend on this relay.

## References and design choices

- [GitHub signature verification](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries): authenticate the original bytes with HMAC-SHA256; verify before processing.
- [GitHub webhook practices](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks): delivery IDs, explicit redelivery, HTTPS, and bounded response time.
- [Go HTTP client contract](https://pkg.go.dev/net/http#Client): bounded request lifetime and explicit redirect policy.
- `server/internal/handler/agent_a2a_forward.go`: the repository's existing environment-forwarding boundary; reused principles without coupling to its registry or business identity.
