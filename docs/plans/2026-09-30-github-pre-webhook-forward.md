# GitHub pre-release webhook forwarding

## Objective and evidence

GitHub cannot connect to `pre-fde-workbench.dingtalk.com`. The new
`qwen-tag-pre` App's original and redelivered ping both failed to connect,
while a locally signed ping returned HTTP 200/pong after pre-release deployment.
The production ingress must accept this App's deliveries and forward them to
pre-release without processing their domain effects in production.

User authorization: create a separate worktree/branch, implement the forwarding
capability on production, retain the existing production App, and complete its
configuration and validation. Another session owns `feat/context-capabilities`.

Worktree: `/private/tmp/dt-fde-multica-github-pre-webhook-relay-20260930`.
Branch: `fix/github-pre-webhook-relay-20260930`.

## Design

See [the current contract](../github-pre-webhook-forward.md). Follow GitHub's
raw-body HMAC verification contract and Go's explicit timeout/no-redirect HTTP
client model; the existing A2A environment forwarding is a local reference for
separating authentication, fixed destinations, and domain execution.

1. Add a dedicated, disabled-by-default production ingress. Its independent
   Secret is the pre-release App's Webhook Secret; the existing production
   webhook route and Secret remain unchanged.
2. Verify bounded raw bytes before any outbound request. Forward only the
   original body and explicit GitHub delivery headers to a fixed canonical
   HTTPS webhook URL. Reject redirects and loops; never relay browser credentials.
3. Acknowledge only after pre-release returns success. Do not retry within the
   forwarding handler and do not execute domain handlers on production.
4. Document and whitelist the two runtime configuration keys. No database
   migrations, Runtime image changes, or frontend changes are needed.

## Implementation and validation

- [x] Write the contract before code.
- [x] Implement route/configuration/handler and focused tests.
- [x] Check raw-body/header preservation, authentication isolation, bounded
      requests, loop/redirect rejection, and failure propagation.
- [ ] Create a dedicated CR and publish this branch, preserving concurrent work.
- [ ] Deploy candidate to pre-release, then production through Aone gates.
- [ ] Set production forwarding configuration and point `qwen-tag-pre`'s
      Webhook to the new production ingress after it is available.
- [ ] Redeliver the real GitHub ping and prove production-to-pre-release success.

Tests are limited to the new authentication/routing boundary and error behavior;
no duplicate tests for the existing PR or installation business handlers.

## Results and remaining work

In progress. Existing App ID `4285068` remains production-owned;
`qwen-tag-pre` is App ID `5131377` and uses pre-release OAuth/setup callbacks.

Focused handler tests and the server router compile check passed. The worktree
base is an ancestor of the currently deployed production release, so merging
this branch does not require promoting unrelated pre-release-only features.
