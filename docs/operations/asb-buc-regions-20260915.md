# ASB BUC startup and execution regions

Verified on 2026-09-15 against the ASB lifecycle API, OSB CLI 0.1.1, and the stable runtime image with digest `sha256:d6994ba38d1c4242a2e247c0f61647d91039ab44a0b0bad0e40b52848cfd4472`.

## Failure and minimal network correction

The production task `596bfdf0-b145-474b-b5ed-ce73e8febe5d` failed during BUC attach, before the task process started. ASB reported HTTP 500. Its wgclient failed to resolve the trust-device registration host, `tp-alilang.alibaba-inc.com`.

Both Hangzhou and Zhangjiakou reproduced that failure with the existing default-deny policy. Adding the registration domain allowed device registration and TUN creation, but nftables still dropped the WireGuard handshake. A temporary trace in an owned diagnostic sandbox identified the literal destinations `140.205.109.26:11940` and `140.205.109.30:11940` (UDP). The trace table was removed after inspection.

The initial minimal correction added exactly:

- `tp-alilang.alibaba-inc.com`
- `140.205.109.26`
- `140.205.109.30`

No wildcard or CIDR allowance is required. The ASB policy format used here permits exact IPs but does not restrict the allowance to a port. If ASB changes its managed gateway pool, update these destinations using fresh transport evidence rather than widening the network range.

A fresh Hangzhou sandbox `4011dc388f334df2815c5c202b081d9d` was created with the original deny policy plus only those three destinations. Attach returned HTTP 200; the real BUC probe subsequently returned `success=true`, `errorCode=0`, the expected employee, and a non-empty Agent identity. An immediate post-attach probe can still report `zt token not found` while DNS/tunnel state converges; the existing bounded probe loop must remain authoritative.

Zhangjiakou returned `zt token not found` even in a fresh unrestricted diagnostic sandbox with the same freshly refreshed credential trio that passed in Hangzhou. Zhangjiakou is not accepted for BUC in this investigation. The user requested no further regional platform debugging.

## Credentials

The live BUC refresh endpoint `/rpc/oauth2/refresh_token.json` returned `access_token`, `refresh_token`, `id_token`, `expires_in`, and `token_type`. The observed AT lifetime was 259200 seconds; the returned ID token lifetime was 7200 seconds. The old comment and tests assuming refresh omitted ID token were stale.

The platform already encrypts credentials in its database and injects them through `wireguard.lazyAuth` plus runtime attach. This combines application-managed credentials with ASB runtime binding and does not rely on a seven-day source sandbox. Refresh now requires and persists the full new trio, with no old-ID-token fallback. Task identity resolution checks ID token expiry independently of AT expiry under the existing cross-replica lock and token-version CAS.

This is renewable authorization, not permanent authorization. The user's ASB support response states a 30-day BUC RT limit and a seven-day sandbox lifetime. No claim is made that refresh extends the overall RT authorization indefinitely. Revocation or an expired RT still requires employee reauthorization.

## Agent region selection

- The execution settings obtain choices from `GET /api/runtimes/{runtimeId}/asb-regions`, backed by the Runtime's actual ASB quota API response.
- A workspace member may discover these non-secret capabilities; normal Agent management permissions govern changes.
- Selection is stored in `runtime_config.asb_regions`. Missing or empty means automatic placement across current API regions. A non-empty array is a strict constraint.
- Save validates selected regions against current API results. Cold creation, quota-refresh failover, and retry all retain the constraint. A full selected region waits; it does not fall back to an unselected one.
- New sandboxes record actual creation region in `multica.region`. Warm sessions outside the selection, including older sessions with unknown placement, are replaced at the next task boundary. Running tasks are not moved.
- BUC users can choose only `cn-hangzhou`.

## Sources and verification

- [ASB identity documentation](https://sandbox.aone.alibaba-inc.com/docs/lifecycle/identity.html)
- [ASB egress documentation](https://sandbox.aone.alibaba-inc.com/docs/network/egress.html)
- [ASB regional documentation](https://sandbox.aone.alibaba-inc.com/docs/region.html)
- Service tests cover ID expiry independent of AT, full-trio replacement, missing-ID rejection, selected-region capacity/failover, and warm-session placement.
- Frontend tests cover API-derived choices, preserving other runtime configuration, empty-selection rejection, and discovery failures. Schema tests cover malformed responses and new API regions.
- Local checks are compilation, static analysis, and unit tests. Deployment and business acceptance are recorded separately against real preproduction.

## Later default-domain expansion

After the minimal-network acceptance, the user requested default access to
`*.alibaba-inc.com` and `*.dingtalk.com`. Both are now built-in allow rules and
accepted at the sandbox creation boundary. The two literal WireGuard gateway
IPs remain necessary because domain wildcards do not cover IP-only traffic.
Default action remains deny. Existing active sandboxes keep their policy until
the next task boundary, when a mismatched policy fingerprint causes replacement.
