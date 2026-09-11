# ASB capacity coordination

Cold task launches and capacity-wait retries share a tenant gate in Redis/Tair,
keyed by a SHA-256 digest of the ASB API origin and credential. The existing
PostgreSQL tenant advisory lock continues to serialize reclaim and creation.
The shared gate caches an unsuccessful capacity result for thirty seconds.
Healthy creation has no fixed delay between holders of the tenant lock, so
short tasks can reach the configured concurrency without waiting five seconds
per sandbox. Cache hits do not refresh the TTL. New tasks obey the same gate as
the background capacity waiter.

The tenant lock still serializes quota checks, safe idle reclamation and the
create request; sandbox boot happens after that lock is released. During a
rolling deployment, old `checking`/`available` Redis markers are ignored by a
new lock holder. A successful request cannot erase a live full/429 verdict.

An ASB HTTP 429 keeps ordinary tasks queued with a blocked startup attempt.
Repeated throttles use a shared 30, 60, 120, 240, then 300 second cooldown.
`Retry-After` in seconds or HTTP-date form can extend that cooldown (up to one
day). A completed, non-throttled capacity probe resets the backoff streak;
older in-flight successes cannot shorten an active throttle cooldown. Warm
sandbox reuse skips capacity checks but respects shared throttle cooldowns.
Request-bound DEAP tasks retain their existing non-resumable behavior.

The waiter retains its thirty-second per-task eligibility delay and one-minute
recovery scan, plus terminal-task wakeups. These schedule launch attempts; they
do not bypass the shared gate. Each replica has at most four recovery launches
in flight. It rotates through tenant FIFO batches before giving another slot
to the same tenant, and one tenant can fill otherwise idle slots while its
earlier sandboxes boot. Task leases prevent duplicate launches across replicas.
Redis must be available through `REDIS_URL` or
the existing Aone Redis credential configuration. Missing or unavailable Redis
keeps launches queued instead of querying ASB independently on every replica.

Only the negative capacity verdict is cached. Sandbox eligibility, ownership,
task idleness and lifecycle state are still checked live before deletion.
There are no database migrations or new runtime configuration keys.

Operational log events:

- `asb_capacity_cooldown_hit`: a task reused a shared wait verdict.
- `asb_capacity_cooldown_recorded`: a full or throttled probe installed a
  shared cooldown; includes `retry_after_ms` and `rate_limited`.
- `asb_capacity_rate_limited`: upstream operation, status, error code and
  request ID for a deferred 429.

Validation covers serialized reservations without fixed waits across independent
PostgreSQL connections and Redis clients, old positive marker handling,
bounded parallel recovery for one tenant, tenant isolation and fair selection,
cooldown expiry without indefinite extension, exponential
backoff and Retry-After, warm reuse semantics, and the real Launcher plus
PostgreSQL path for full quota followed by a Running-list 429. The task stays
queued, a second replica makes no ASB requests during cooldown, and creation
can proceed after cooldown once capacity is available.
