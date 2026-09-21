# Aone frontend recovery

The frontend supervisor owns the Next.js process and port 6001 health server.
It restarts exited children with 1–60 second exponential backoff. Three failed
5-second HTTP probes after a 30-second startup grace terminate a hung child;
after 5 seconds without exit it escalates to SIGKILL. The backend stays running.
Planned shutdown stops the supervisor before stopping application processes.
The existing Aone automatic migration runs once before starting the supervisor;
frontend restarts never invoke migrations.

`/health`, `/check.node`, and `/status.taobao` check both the backend and an actual
frontend `/login` render. Non-2xx/3xx results and responses exceeding 1.5 seconds
are unhealthy. `/healthz` remains backend-only. This change does not claim to
install Kubernetes probes: the current Aone trait catalog does not expose them.
VIPServer and Aone consume the aggregate health paths.

Nginx allows at most 64 active dynamic frontend connections per replica. Requests
identifying as Alibaba.Security.Heimdall share a 1 request/second bucket, a burst
of 2, and at most 2 simultaneous requests per replica. These are admission limits,
not authentication or an assertion of scanner identity. API, daemon streams,
health probes and immutable static assets are not in these limits.

## Operational events

The supervisor writes JSON events to `health.log` and the existing collected
`backend.log`: `frontend_exited` (ERROR), `frontend_unhealthy` (ERROR), and
`frontend_recovered` (INFO). Next.js emits memory samples every 30 seconds and
`frontend_heap_pressure` (WARN) at 85% of the V8 heap limit, also copied to
`backend.log`. An OOM exit is recorded with its exit code/signal and retained
stderr; SIGABRT alone is not labelled definitively as OOM.

Use SLS query `frontend_exited or frontend_unhealthy or frontend_heap_pressure`
for the application's alarm source. Notification destinations are platform
configuration, not embedded credentials or webhooks in the application. Events
are observable through the existing authenticated `/api/internal/logs/tail`
endpoint (`file=health`, `frontend` or `backend`). Never log request bodies,
cookies or environment values. No diagnostic HTTP mutation endpoint is exposed.

`run/frontend-status.json` records the last sampled health, current PID and
restart count. `run/frontend.pid` always belongs to the current child. Frontend
logs are appended and rotated to `frontend.log.previous` at 10 MiB on restart,
so the crash evidence survives recovery.

## Preproduction acceptance

1. Verify both replicas run Next.js 16.2.12 and return healthy aggregate checks.
2. Send a bounded burst of read-only requests using a synthetic Heimdall user
   agent. Verify 429 responses and that ordinary login/API/health requests work.
3. On one preproduction replica, read `run/frontend.pid`, send SIGKILL to that
   PID, and observe an ERROR event, a new PID and successful login/health probes.
4. Repeat with SIGSTOP to verify timeout-driven SIGTERM/SIGKILL recovery.
5. Verify backend PID remains unchanged, recovery events are queryable, then
   repeat the crash check on the other replica. Do not fault-inject production.

Local unit validation: `node --test src/frontend-supervisor.test.cjs`.
