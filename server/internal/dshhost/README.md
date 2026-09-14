# Employee DSH host coordination

This package owns employee storage and native execution identities for FC DSH.
The FC launcher now uses it for employee admission, mount initialization and
native Host readiness. The task adapter and native UI gateway are still being
integrated; the new image capability is not advertised or deployed yet.

AgenticFS was mounted as NFS v3 with `nolock,local_lock=all` in the real FC probe.
File and SQLite locks do not exclude a writer in a different sandbox. The
ownership record therefore has no expiry-based takeover path:

```
offline -> creating -> running -> retiring -> offline
             |                       |
       reconcile same intent    confirm old ID absent
```

Creation intent and generation are committed in PostgreSQL before the provider
request. Concurrent replicas compete through conditional updates. A request
timeout, empty listing, missing response, or crashed caller leaves the intent
in place. Reconciliation adopts exactly one matching intent, employee, volume,
access point, requested template and role; it never retries creation.

Retirement must be called after task admissions stop and tasks drain. A DELETE
acknowledgement is insufficient: a subsequent GET of the exact sandbox must
return 404 before the store permits a new generation. Old generation receipts
cannot clear a replacement. Storage registration is immutable and unique by
volume, access point and file-system/AgenticSpace pair. Provisioning must verify
the provider-side resources and employee authorization before registration.

`BindExecution` commits a native Session per employee and conversation scope,
then an immutable request ID per task, before external prompt admission. All
replicas and replacement sandboxes reuse those records. Issue scope has the
same precedence as task serialization; chat scope follows it. Unscoped tasks
receive separate Sessions. Native working directories derive from the persisted
Session ID under the employee mount. Rebinding an existing task to a different
Session fails. Prompt retry must use the persisted request ID, since native DSH
checks that identity against accepted user messages. The binding itself is not
proof that a prompt was admitted or completed; task receipt and trajectory
reconciliation remain required.

## FC API behavior verified on 2026-09-14

- POST `/sandboxes` uses `volumeMounts: [{name, path}]`, explicit VPC and role
  metadata, and `autoPause: false`. Omit `autoResume`; it is an object when used.
- GET returns the display alias in `templateID`, not the immutable ID supplied
  at creation, and consumes `fc.sandbox.*` metadata. Independent intent labels
  preserve the requested template and role for reconciliation.
- `/v2/sandboxes` listing can lag direct GET. A bounded caller may retry reads
  for the same intent. Listing invisibility is never evidence of destruction.
- A real test discarded the successful create response, adopted the original
  sandbox through another PostgreSQL pool, reused it, confirmed destruction,
  created generation 2 and confirmed its cleanup.

## Verification

Run `DSH_HOST_TEST_DATABASE_URL=<isolated PostgreSQL> go test -race ./internal/dshhost`
from `server`. Tests use isolated schemas and separate connection pools, replay
the migrations twice, and clean up their own schemas. They cover 24 competing
callers, ambiguous create outcomes, old timestamps, health failure, unconfirmed
destruction, stale generation receipts, request cancellation and storage aliases.

The live test requires explicit `DSH_HOST_LIVE_TEST=1`, FC credentials in the
process environment, and the `DSH_TEST_*` settings listed in `fc_test.go`. Use an
authorized probe volume only. It creates two temporary sandboxes and destroys
them; it does not execute DSH or prove application-level recovery.

Before deployment, complete provisioning intents, native activity draining,
credentials scoped to executions, the persistent task adapter, profile revisions
and the native gateway, then verify the full chain in FC and pre-release.
