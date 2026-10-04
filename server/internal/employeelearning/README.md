# Employee background learning consumption

`Store.Process` consumes only terminal Runs owned by Employee. Discovery reads IDs;
for each candidate, the transaction locks workspace → Task/Run before invoking the
Host capture callback. Host verifies the current agent/tenant and queue binding
before reading the private goal/result. Ordinary runtime claims are stored through
`employeememory.RecordTx` as requester-private, confidence-3 `inferred` records;
queue completion is never `VerifiedExecution` or `Passed` proof.

The Run ID is the durable consumption key. Learning and its consumption receipt
commit together. Permanent invalid candidates retain a `skipped` reason, including
stale goal revision, stale tenant, archived agent, missing requester/execution,
empty result and rejected learning text. A Host-supplied completion timestamp is
checked under the memory namespace lock; reset cannot be undone by a first late
capture or by an old replay. Transient database errors remain retryable.

The existing Employee worker polls bounded batches independently of foreground
model work. No LLM or network operation is performed by this consumer. Captured
private briefs are read only when every message in the current admitted window
has one identical, known requester. Mixed/unknown windows load scene memory only.
Private scope never becomes scene scope, and a payload actor is not authorization.

This slice does not implement human-stated correction capture, verified-run
promotion, or arbitrary model-written learning. Tests in
`handler/employee_learning_capture_test.go` exercise real PostgreSQL capture,
restart/replay, two-connection reset races, atomic rollback and private recall.
