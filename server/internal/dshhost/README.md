# Employee DSH host coordination

This package owns employee storage and native execution identities for FC DSH.
The FC launcher uses it for employee admission, mount initialization and native
Host readiness. The native task adapter and storage provisioning entry point
are integrated in the feature branch; the native UI gateway remains unfinished. The FC image
advertises the capability on the candidate branch; deployment and acceptance
must be checked against the actual template catalog.

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

Local checks are compilation, static checks and unit tests only. Do not start a
local application or database for this integration. Run database and native Host
acceptance in the actual pre-release environment or its authorized FC sandboxes.

In that environment, run
`DSH_HOST_TEST_DATABASE_URL=<isolated PostgreSQL> go test -race ./internal/dshhost`
from `server`. Tests use isolated schemas and separate connection pools, replay
the migrations twice, and clean up their own schemas. They cover 24 competing
callers, ambiguous create outcomes, old timestamps, health failure, unconfirmed
destruction, stale generation receipts, request cancellation and storage aliases.

The live test requires explicit `DSH_HOST_LIVE_TEST=1`, FC credentials in the
process environment, and the `DSH_TEST_*` settings listed in `fc_test.go`. Use an
authorized probe volume only. It creates two temporary sandboxes and destroys
them; it does not execute DSH or prove application-level recovery.

Native Host readiness includes a `managed_profile_digest` over the versioned
image overlay contract and model catalog. The supervisor also checks the live
task-context plugin's catalog over the authenticated native API. A changed
catalog requires draining and retiring the old sandbox; it never rewrites a
running Host's configuration. This receipt is separate from the employee's
editable Profile revision, which still needs its durable lifecycle.

Before deployment, complete provisioning intents, native activity draining,
credentials scoped to executions, the persistent task adapter, profile revisions
and the native gateway, then verify the full chain in FC and pre-release.

## Storage provisioning intent

`Provisioner` records operator-owned placement in `dsh_storage_provision` before
any cloud mutation. Each of Space, access point, role, policy, policy attachment
and volume has a separate conditional `planned -> creating -> planned` step.
The intent and step form the provider correlation key. Lost create responses or
failed receipt writes leave `creating` in place; another replica only looks up
the original object. A missing lookup does not authorize another create.

Role creation and permission attachment deliberately have different crash
boundaries. An identity alone does not prove its permissions were applied.
After all six receipts, the provider must verify the complete resource chain,
employee root, enforced RAM access, role restrictions, UID/GID and volume team.
Only then may immutable `BindStorage` expose the Home to the launcher. Final
binding is idempotent if the status write is lost. Placement changes do not
rewrite an existing provisioning intent.

The human-only `GET/POST /api/agents/{id}/dsh-home` entry point additionally
requires employee management permission and an FC DSH runtime. The POST accepts
no placement, resource IDs or credentials. `MULTICA_DSH_STORAGE_CONFIG` selects
one frozen placement and an Aone-managed `credential_resource` access-package
URN for the same account. The application binding and least-privilege cloud
authorization must be provisioned before enabling this configuration. The
setting is passed through the Aone entrypoint and takes effect on deployment.

`CloudStorageProvider` implements NAS, RAM and FC POP operations; `ACSClient`
uses the official SDK signer with request cancellation, bounded response sizes,
no redirect and no operation retry. Dependency readiness checks precede the
conditional creation claim. Reconciliation rejects duplicates and malformed or
cyclic pagination. Final verification compares the employee root and quota,
active AP with RAM enabled, FC-only role trust, its exact AP-scoped policy and
exclusive policy attachment, and the available volume's Team and UID/GID.

No employee storage has been provisioned by this code yet. Unit tests cover
ambiguous outcomes at every step, hidden
listings, lost database receipts, cancellation and failed ownership verification.
`TestProvisionPostgresCompetingReplicas` additionally requires the real
preproduction database; a local skip is not evidence for its SQL behavior.
