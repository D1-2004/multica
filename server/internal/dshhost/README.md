# Employee DSH host coordination

This package is the storage ownership foundation for FC DSH integration. It is
not yet wired into task admission, the FC launcher, or the native UI gateway.
Do not enable a shared writable Home through the existing per-chat launcher.

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
volume and access point. Provisioning must additionally verify the backing
AgenticSpace belongs exclusively to this employee.

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

Before production use, complete provisioning intents, task admission/draining,
mount and UID checks, host process startup, credentials scoped to executions,
session recovery, and the native gateway. These remain separate acceptance gates.
