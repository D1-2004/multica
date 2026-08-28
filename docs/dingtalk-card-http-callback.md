# DingTalk interactive-card HTTP callback

Multica exposes this public callback endpoint:

```text
POST /api/dingtalk/card/customer-feedback
```

The public URL is the deployment's `MULTICA_PUBLIC_URL` plus the path above.
The route is served by the Go backend through the existing Aone nginx `/api/`
proxy.

## Transport contract

The endpoint is intentionally public and performs no authentication or
signature verification. The adapter only performs transport delivery:

1. Forward the incoming request body bytes unchanged to `AITABLE_WEBHOOK_URL`
   with the incoming `Content-Type`.
2. Treat a 2xx AI Table response containing JSON `{"success": true}` as
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

Missing runtime configuration returns HTTP 503. A non-2xx response, malformed
response, `success` other than `true`, or a network failure from AI Table
returns HTTP 502 without `cardData`, so the card is not marked as submitted.

## Runtime configuration

Set the webhook URL through the deployment's secure runtime configuration:

```text
AITABLE_WEBHOOK_URL
```

The name is included in `src/main.sh`'s `RUNTIME_CONFIG_KEYS` whitelist so every
replica receives the same value from the Aone environment trait. Never
hard-code, commit, print, or log the value.

## DingTalk registration

Register or update a `callbackRouteKey` through DingTalk's card callback
registration API. Set `callbackUrl` to the environment's public callback URL,
and do not configure an `apiSecret` for this anonymous endpoint. Updating an
existing route requires the platform's explicit `forceUpdate` confirmation
flow.

## History

| Date | Change | Reason |
| --- | --- | --- |
| 2026-08-28 | Added the public HTTP callback, raw-body AI Table forwarding, transport signature verification, and runtime configuration contract. | Replace the externally hosted adapter with the Aone-managed Multica public ingress while removing customer-feedback-specific business filtering. |
| 2026-08-28 | Removed DingTalk transport signature verification and the callback-secret runtime dependency. | The callback route is explicitly required to accept anonymous requests; only the downstream AI Table success result controls whether the card is marked submitted. |
