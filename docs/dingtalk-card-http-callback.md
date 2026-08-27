# DingTalk interactive-card HTTP callback

Multica exposes this public callback endpoint:

```text
POST /api/dingtalk/card/customer-feedback
```

The public URL is the deployment's `MULTICA_PUBLIC_URL` plus the path above.
The route is served by the Go backend through the existing Aone nginx `/api/`
proxy.

## Transport contract

The adapter performs only transport authentication and delivery:

1. Read `x-ddpaas-signature-timestamp` and `x-ddpaas-signature`.
2. Compute Base64-encoded HMAC-SHA256 over the timestamp using
   `DINGTALK_CARD_CALLBACK_SECRET` and compare it in constant time.
3. Forward the incoming request body bytes unchanged to `AITABLE_WEBHOOK_URL`
   with the incoming `Content-Type`.
4. Treat a 2xx AI Table response containing JSON `{"success": true}` as
   success.

The adapter does not parse, filter, whitelist, or rebuild card action IDs,
forms, project rows, satisfaction fields, or any other business payload.
Request bodies and secret values are never written to application logs.

On AI Table success, the endpoint returns HTTP 200 with this DingTalk card
update:

```json
{
  "cardUpdateOptions": {
    "updateCardDataByKey": true,
    "updatePrivateDataByKey": false
  },
  "cardData": {
    "cardParamMap": {
      "formState": "disabled",
      "formDisabled": "true",
      "submitButtonText": "已提交"
    }
  }
}
```

An invalid signature returns HTTP 401. Missing runtime configuration returns
HTTP 503. A non-2xx response, malformed response, `success` other than `true`,
or a network failure from AI Table returns HTTP 502 without `cardData`, so the
card is not marked as submitted.

## Runtime configuration

Set both values through the deployment's secure runtime configuration:

```text
AITABLE_WEBHOOK_URL
DINGTALK_CARD_CALLBACK_SECRET
```

Both names are included in `src/main.sh`'s `RUNTIME_CONFIG_KEYS` whitelist so
every replica receives the same values from the Aone environment trait. Never
hard-code, commit, print, or log either value.

## DingTalk registration

Register or update a `callbackRouteKey` through DingTalk's card callback
registration API. Set `callbackUrl` to the environment's public callback URL,
and use the same secret value for the registration `apiSecret` and
`DINGTALK_CARD_CALLBACK_SECRET`. Updating an existing route requires the
platform's explicit `forceUpdate` confirmation flow.

## History

| Date | Change | Reason |
| --- | --- | --- |
| 2026-08-28 | Added the public HTTP callback, raw-body AI Table forwarding, transport signature verification, and runtime configuration contract. | Replace the externally hosted adapter with the Aone-managed Multica public ingress while removing customer-feedback-specific business filtering. |
