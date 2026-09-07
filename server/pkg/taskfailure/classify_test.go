package taskfailure

import "testing"

// TestClassifyEmptyAndWhitespace pins the empty/whitespace contract.
// Daemon callers should never hand us empty error text — but if they
// do, returning the catchall is safer than panicking.
func TestClassifyEmptyAndWhitespace(t *testing.T) {
	t.Parallel()

	cases := []string{"", "   ", "\n\t  \n"}
	for _, in := range cases {
		if got := Classify(in); got != ReasonAgentUnknown {
			t.Errorf("Classify(%q) = %q, want %q", in, got, ReasonAgentUnknown)
		}
	}
}

// TestClassifyRules walks every classifier rule with a real-world
// sample taken from MUL-1949's db-boy production analysis (top error
// prefixes from `agent_task_queue.error` over a 7-day window). When
// MUL-1949's SQL grows a new rule, add a fixture here so the in-flight
// classifier and the offline backfill stay in lock-step.
//
// One test case per rule is the minimum bar; rules with notable
// boundary conditions (e.g. the 5xx regex) get a dedicated subtest
// further down.
func TestClassifyRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want Reason
	}{
		// 1. Context overflow.
		{"context length exceeded", "Error: context length exceeded for model gpt-4", ReasonAgentContextOverflow},
		{"context_length_exceeded code", `{"error":{"code":"context_length_exceeded"}}`, ReasonAgentContextOverflow},
		{"maximum context", "Maximum context window of 200000 tokens has been exceeded", ReasonAgentContextOverflow},
		{"prompt is too long", "API Error: prompt is too long: 250000 tokens > 200000 maximum", ReasonAgentContextOverflow},
		{"context size has been exceeded", "context size has been exceeded; consider /compact", ReasonAgentContextOverflow},
		{"token limit", "Hit the token limit for this conversation", ReasonAgentContextOverflow},
		// GH #6360, verbatim from Claude Code 2.1.x. The turn is not
		// rejected with a 400 — the response comes back with stop_reason
		// "model_context_window_exceeded" and the CLI prints this line.
		{"claude code context window limit", "API Error: The model has reached its context window limit.", ReasonAgentContextOverflow},
		{"raw stop reason", `{"stop_reason":"model_context_window_exceeded"}`, ReasonAgentContextOverflow},

		// 2. Missing config.
		{"missing env var", "Missing environment variable: `MIFY_API_KEY`.", ReasonAgentMissingConfig},
		{"missing api_key", "Failed to authenticate: missing api_key in config", ReasonAgentMissingConfig},
		{"api key required", "An api key is required to use this provider", ReasonAgentMissingConfig},
		{"no llm provider configured", "no llm provider configured; set OPENAI_API_KEY", ReasonAgentMissingConfig},
		{"no provider configured", "no provider configured for runtime", ReasonAgentMissingConfig},

		// 3. Provider auth / access.
		{"401", "API Error: 401 Unauthorized", ReasonAgentProviderAuthOrAccess},
		{"403", "API Error: 403 Forbidden", ReasonAgentProviderAuthOrAccess},
		{"unauthorized text", "Request unauthorized for this organization", ReasonAgentProviderAuthOrAccess},
		{"login required", "login required: please run /login", ReasonAgentProviderAuthOrAccess},
		{"not logged in", "Not logged in · Please run /login", ReasonAgentProviderAuthOrAccess},
		{"please login again", "Session expired, please login again", ReasonAgentProviderAuthOrAccess},
		{"refresh token", "refresh token has expired", ReasonAgentProviderAuthOrAccess},
		{"invalid api key", "Invalid API key provided", ReasonAgentProviderAuthOrAccess},
		{"access token", "access token has been revoked", ReasonAgentProviderAuthOrAccess},
		{"subscription access", "Your organization has disabled Claude subscription access for Claude Code", ReasonAgentProviderAuthOrAccess},
		{"does not have access", "Your account does not have access to this model", ReasonAgentProviderAuthOrAccess},
		{"may not have access", "you may not have access to claude-3-opus", ReasonAgentProviderAuthOrAccess},

		// 4. Provider quota / billing.
		{"402", "API Error: 402 Payment Required", ReasonAgentProviderQuotaLimit},
		{"insufficient_balance", `{"error":{"code":"insufficient_balance"}}`, ReasonAgentProviderQuotaLimit},
		{"balance is too low", "balance is too low to make this request", ReasonAgentProviderQuotaLimit},
		{"monthly usage limit", "You've hit your org's monthly usage limit", ReasonAgentProviderQuotaLimit},
		{"usage limit", "Account exceeded the daily usage limit", ReasonAgentProviderQuotaLimit},
		{"hit your limit ascii", "you've hit your limit; upgrade to continue", ReasonAgentProviderQuotaLimit},
		{"hit your limit curly", "you\u2019ve hit your limit", ReasonAgentProviderQuotaLimit},
		{"credits", "Your account has 0 credits remaining", ReasonAgentProviderQuotaLimit},
		{"quota", "quota exceeded for project foo", ReasonAgentProviderQuotaLimit},

		// 5. Capacity / rate limit.
		{"429", "API Error: 429 Too Many Requests", ReasonAgentProviderCapacityOrRateLimit},
		{"529", "Server overloaded: HTTP 529", ReasonAgentProviderCapacityOrRateLimit},
		{"rate limit", "rate limit exceeded for tier 3", ReasonAgentProviderCapacityOrRateLimit},
		{"overloaded", "overloaded_error: please retry", ReasonAgentProviderCapacityOrRateLimit},
		{"no capacity available", "no capacity available; try again later", ReasonAgentProviderCapacityOrRateLimit},

		// 6. Provider 5xx / server error.
		{"server had an error", "the server had an error processing your request", ReasonAgentProviderServerError},
		{"provider returned error", "provider returned error: malformed response", ReasonAgentProviderServerError},
		{"internal error", "An internal error occurred while serving the request", ReasonAgentProviderServerError},
		{"500 with delimiter", "API Error: 500 Internal Server Error", ReasonAgentProviderServerError},
		{"503 anywhere", "got HTTP 503 from provider", ReasonAgentProviderServerError},
		{"503 at start", "503 service degraded", ReasonAgentProviderServerError},
		{"504 at end", "upstream returned 504", ReasonAgentProviderServerError},
		{"service unavailable", "service unavailable, retry later", ReasonAgentProviderServerError},
		{"bad gateway", "Bad Gateway: upstream rejected", ReasonAgentProviderServerError},

		// 7. Provider network.
		{"stream disconnected", "stream disconnected before completion", ReasonAgentProviderNetwork},
		{"connection closed mid-response", "API Error: Connection closed mid-response. The response above may be incomplete.", ReasonAgentProviderNetwork},
		{"connection closed with exit status wins over process failure", "claude exited with error: exit status 1\nAPI Error: Connection closed mid-response.", ReasonAgentProviderNetwork},
		{"error sending request", "error sending request for url (https://api.example.com/v1)", ReasonAgentProviderNetwork},
		{"unable to connect", "unable to connect to provider", ReasonAgentProviderNetwork},
		{"dial tcp", "dial tcp 1.2.3.4:443: connect: connection refused", ReasonAgentProviderNetwork},
		{"connection refused alone", "connection refused", ReasonAgentProviderNetwork},
		{"connectionrefused single", "ConnectionRefused", ReasonAgentProviderNetwork},
		{"dns", "dns lookup failed", ReasonAgentProviderNetwork},
		{"i/o timeout", "read tcp 1.2.3.4:443: i/o timeout", ReasonAgentProviderNetwork},
		// MUL-5370: every Go-side context deadline used to land in
		// agent_error.unknown, which is not on the retry allowlist — a
		// transient stall became a terminal failure with a useless label.
		{"context deadline exceeded", "context deadline exceeded", ReasonAgentProviderNetwork},
		{"wrapped context deadline", `Post "https://api.example.com/v1": context deadline exceeded`, ReasonAgentProviderNetwork},
		{"http client timeout", `Get "https://api.example.com": net/http: request canceled (Client.Timeout exceeded while awaiting headers)`, ReasonAgentProviderNetwork},
		// #6522: all three OpenCode terminal-signal guard failures are silent
		// provider stream cuts. The two "terminal signal" variants used to hit
		// rule 13 by accident (the word "signal") and the empty-step one fell
		// to agent_error.unknown; neither bucket is retryable.
		{"opencode step open at EOF", "opencode stream ended without a terminal signal (step still open at EOF)", ReasonAgentProviderNetwork},
		{"opencode continuation never started", "opencode stream ended without a terminal signal (last step required a continuation that never started)", ReasonAgentProviderNetwork},
		{"opencode empty final step", "opencode stream ended on an empty step (no text, no tool call, no reported usage) — the provider produced nothing", ReasonAgentProviderNetwork},
		{"opencode empty step with process exit appended", "opencode stream ended on an empty step (no text, no tool call, no reported usage) — the provider produced nothing; opencode exited with error: exit status 1", ReasonAgentProviderNetwork},
		// The OpenCode backend appends a request-size bracket to these messages
		// (opencodeRequestSizeSuffix in pkg/agent/opencode.go) so a dead stream
		// says how big the request was. Byte and token counts land on digit
		// boundaries, so without the prefix witness checked ahead of the switch
		// a 512-byte prompt matches the 5xx regex (rule 6) and a 429-token step
		// matches the capacity regex (rule 5) — silently re-bucketing the
		// failure off the retry allowlist because of how large the request
		// happened to be. These three are the shapes that would break it.
		{"opencode enriched with 5xx-shaped byte count", "opencode stream ended on an empty step (no text, no tool call, no reported usage) — the provider produced nothing [request: 512 prompt bytes on stdin]", ReasonAgentProviderNetwork},
		{"opencode enriched with 429-shaped token count", "opencode stream ended on an empty step (no text, no tool call, no reported usage) — the provider produced nothing [request: 19563 prompt bytes on stdin; last accepted step reported 429 input tokens across 3 steps]", ReasonAgentProviderNetwork},
		{"opencode enriched with process exit after the bracket", "opencode stream ended without a terminal signal (step still open at EOF) [request: 19563 prompt bytes on stdin; last accepted step reported 14585 input tokens across 2 steps]; opencode exited with error: exit status 1", ReasonAgentProviderNetwork},

		// 8. Model not found / unavailable.
		{"model not found", "Error: model claude-3-opus-99 not found", ReasonAgentModelNotFoundOrUnavailable},
		{"model not found phrase", "the model was not found in this account", ReasonAgentModelNotFoundOrUnavailable},
		{"unknown model", "unknown model 'foo-1.0'", ReasonAgentModelNotFoundOrUnavailable},
		{"selected model", "the selected model is no longer supported", ReasonAgentModelNotFoundOrUnavailable},
		{"http 404", "HTTP 404: model endpoint not registered", ReasonAgentModelNotFoundOrUnavailable},
		{"404 page not found", "404 page not found", ReasonAgentModelNotFoundOrUnavailable},

		// 9. Empty / unparseable output.
		{"returned empty output", "openclaw returned empty output", ReasonAgentEmptyOrUnparseableOutput},
		{"returned no parseable output", "kimi returned no parseable output", ReasonAgentEmptyOrUnparseableOutput},

		// 10. Agent timeout.
		{"timed out after", "claude timed out after 2h0m0s", ReasonAgentTimeout},

		// 11. Runtime missing executable.
		{"executable not found", "executable not found in $PATH", ReasonAgentRuntimeMissingExecutable},

		// 12. Runtime version unsupported.
		{"below the minimum supported version", "claude CLI 0.1.0 is below the minimum supported version 0.5.0", ReasonAgentRuntimeVersionUnsupported},
		{"requires a newer version", "this protocol requires a newer version of the runtime", ReasonAgentRuntimeVersionUnsupported},

		// 13. Process failure.
		{"exit status", "agent exit status 137", ReasonAgentProcessFailure},
		{"signal", "agent terminated by signal: killed", ReasonAgentProcessFailure},
		{"panic", "panic: runtime error: invalid memory address", ReasonAgentProcessFailure},
		{"sigsegv", "fatal error: SIGSEGV", ReasonAgentProcessFailure},
		{"process exited", "process exited with status 1", ReasonAgentProcessFailure},
		{"pipe has been ended", "the pipe has been ended", ReasonAgentProcessFailure},
		{"file already closed", "write |1: file already closed", ReasonAgentProcessFailure},
		{"initialize failed", "initialize failed: backend not ready", ReasonAgentProcessFailure},

		// 14. Catchall.
		{"unrecognized", "the agent gave up for reasons unknown", ReasonAgentUnknown},
		{"sentence with no marker", "Hello world.", ReasonAgentUnknown},

		// 15. Digit-boundary regression: 3-digit HTTP status codes must NOT
		//     match when embedded in a longer number. Before the fix these
		//     landed in provider auth/quota/capacity buckets, masking hard
		//     process failures under a provider reason and polluting failure
		//     observability.
		{"402 embedded not quota", "agent consumed 402913 tokens before crashing", ReasonAgentUnknown},
		{"529 embedded not capacity", "request latency was 15290ms; then it panicked: signal killed", ReasonAgentProcessFailure},
		{"403 embedded not auth", "processed 4030 items, then exit status 1", ReasonAgentProcessFailure},
		{"401 embedded not auth", "job 24019 finished, process exited with status 2", ReasonAgentProcessFailure},
		{"429 embedded not capacity", "seq 14290 unknown outcome", ReasonAgentUnknown},
		// Genuine status codes with a boundary still classify correctly.
		{"402 boundary still quota", "API Error: 402 Payment Required", ReasonAgentProviderQuotaLimit},
		{"403 boundary still auth", "HTTP 403 Forbidden", ReasonAgentProviderAuthOrAccess},
		{"429 boundary still capacity", "got 429 from provider", ReasonAgentProviderCapacityOrRateLimit},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.in); got != c.want {
				t.Fatalf("Classify(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestClassifyOrderingPriorities pins the rule precedence between
// overlapping rules. These cases caught regressions during MUL-2946 PR1
// review: the SQL CASE ordering matters and a naive Go switch could
// silently route them differently.
func TestClassifyOrderingPriorities(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want Reason
	}{
		// "token limit" mentions both "context-ish" tokens AND
		// "limit". The context_overflow rule must win because the
		// quota-limit rule's "limit" trigger would otherwise swallow
		// it.
		{"token limit beats quota", "you exceeded the token limit", ReasonAgentContextOverflow},

		// 401 + missing api_key: the missing_config rule runs before
		// auth precisely so we don't classify a config error as an
		// auth rejection.
		{"missing api key beats 401", "missing api_key for openai (401 returned downstream)", ReasonAgentMissingConfig},

		// Both "429" and "rate limit" present — should still land in
		// the capacity bucket, not the quota bucket.
		{"429 rate limit", "API Error: 429 rate limit reached", ReasonAgentProviderCapacityOrRateLimit},

		// "exit status" co-occurring with a stronger upstream marker
		// — the upstream classification should win because the
		// process_failure rule is checked last.
		{"exit status with 401 upstream", "exit status 1: API Error: 401 Unauthorized", ReasonAgentProviderAuthOrAccess},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.in); got != c.want {
				t.Errorf("Classify(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestClassify5xxRegex pins the boundary behavior of the 5xx HTTP
// status detector. The SQL classifier uses an anchored regex
// `(^|[^0-9])5[0-9][0-9]([^0-9]|$)`; this Go classifier mirrors it via
// providerHTTP5xxRe. Without the anchors, "1500ms" and "1.5.0" would
// be misclassified as a server error.
func TestClassify5xxRegex(t *testing.T) {
	t.Parallel()

	hits := []string{
		"503",
		" 504 ",
		"got 502 from upstream",
		"upstream returned 599\n",
	}
	for _, in := range hits {
		if got := Classify(in); got != ReasonAgentProviderServerError {
			t.Errorf("Classify(%q) = %q, want %q", in, got, ReasonAgentProviderServerError)
		}
	}

	misses := []string{
		"1500ms latency observed",
		"version 1.5.0 unsupported",
		"5000 tokens generated",
		"agent slept for 1500 seconds",
	}
	for _, in := range misses {
		if got := Classify(in); got == ReasonAgentProviderServerError {
			t.Errorf("Classify(%q) = %q, want NOT provider_server_error", in, got)
		}
	}
}

// TestClassifyAlwaysReturnsAgentSide guarantees Classify never returns
// a platform-side reason. Platform-side reasons originate from
// sweepers / scheduler / poisoned classifier paths that don't pass
// through Classify; the in-flight classifier's job is exclusively to
// pick among the 14 agent_error.* sub-reasons (or fall back to
// ReasonAgentUnknown). A future change that accidentally returned,
// say, ReasonRuntimeOffline from Classify would break Prometheus
// label semantics — pin it here.
func TestClassifyAlwaysReturnsAgentSide(t *testing.T) {
	t.Parallel()

	samples := []string{
		"",
		"random text",
		"401 Unauthorized",
		"context length exceeded",
		"503 internal server error",
		"timed out after 2h0m0s",
		"exit status 1",
	}
	for _, s := range samples {
		got := Classify(s)
		if !got.IsAgentError() {
			t.Errorf("Classify(%q) = %q, must be agent_error.* (an agent error string describes the agent process)", s, got)
		}
	}
}

// TestClassifyDshPluginPreparation pins the one deliberate exception to the
// rule above, with the two error strings that produced it.
//
// Both were observed on 预发 and both were classified wrongly, in opposite
// directions, by rules that scan the whole text for status codes:
//
//   - the fetch failure carries "403", so rule 3 called it provider_auth_or_
//     access and told the operator to re-authenticate a model account that was
//     working perfectly;
//   - the mount failure carries a Node stack trace, and one of its line numbers
//     read as a capacity code, so rule 5 called it provider_capacity_or_rate_
//     limit, which reads as "the provider is busy, try later".
//
// Neither failure involves the model at all: the process dies before the first
// request, and no amount of waiting or re-authenticating changes that. Platform-
// side is the honest bucket, and it is why this classifies ahead of every
// digit-scanning rule.
func TestClassifyDshPluginPreparation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "fetch failure, carrying the HTTP status it failed with",
			raw: "failed to fetch plugin dsh-mcp-lens-uploaded: " +
				"HTTP Error 403: Forbidden",
		},
		{
			name: "mount failure, carrying a Node stack trace",
			raw: "plugin tree failed to load: failed to apply loader entry " +
				"include (cordis:include): failed to import loader entry " +
				"mcp-lens (dsh-mcp-lens): Cannot find package 'dsh-mcp-lens'\n" +
				"    at packageResolve (node:internal/modules/esm/resolve:429:9)\n" +
				"    at moduleResolve (node:internal/modules/esm/resolve:529:18)",
		},
		{name: "digest mismatch", raw: "plugin demo integrity mismatch: got sha256-abc"},
		{name: "unreadable manifest", raw: "plugin demo has invalid package.json"},
		{name: "absent manifest", raw: "plugin demo has no package.json"},
		{name: "unresolvable source", raw: "npm: plugin source must pin a version: npm:demo"},
		{name: "missing local file", raw: "plugin file not found: /tmp/demo.tgz"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.raw)
			if got != ReasonDshPluginUnavailable {
				t.Errorf("Classify(%q) = %q, want %q", c.raw, got, ReasonDshPluginUnavailable)
			}
			if got.IsAgentError() {
				t.Errorf("%q must be platform-side: the model was never called", got)
			}
		})
	}
}

// TestClassifyDshPluginWitnessesAreSpecific guards the other direction, and it
// is the more important direction.
//
// One caller hands Classify the agent's own comment text (the MUL-2946 branch
// in daemon/daemon.go), which is prose a model wrote about whatever it was
// working on. "the repo has no package.json" is an ordinary sentence there. A
// witness loose enough to match it would relabel that agent's real failure as a
// plugin failure — moving it off the retry path and pointing the operator at a
// plugin that had nothing to do with it. A missed plugin failure costs a wrong
// label; a stolen agent failure costs a retry, so the witnesses are written to
// be precise rather than generous.
// TestContextOverflowOutranksThePluginGuard pins the one ordering the guard
// must lose.
//
// A backend that has already proven the context is exhausted appends the
// model's own text to its error, and that text can say anything — including
// something that reads like a plugin problem. Losing context_overflow there
// would cost more than a label: it is on the resume blacklist and
// dsh_plugin_unavailable is not, so the exhausted session would stay pinned as
// the resume pointer and every later turn would replay the same overflow.
func TestContextOverflowOutranksThePluginGuard(t *testing.T) {
	t.Parallel()

	raw := "agent terminated (terminal_reason=prompt_too_long): prompt is too long. " +
		"detail: plugin tree failed to load"
	if got := Classify(raw); got != ReasonAgentContextOverflow {
		t.Errorf("Classify(%q) = %q, want %q", raw, got, ReasonAgentContextOverflow)
	}
}

func TestClassifyDshPluginWitnessesAreSpecific(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want Reason
	}{
		// Provider failures must still reach their own rules.
		{"provider auth", "401 Unauthorized", ReasonAgentProviderAuthOrAccess},
		{"provider capacity", "429 rate limit exceeded", ReasonAgentProviderCapacityOrRateLimit},

		// Prose an agent plausibly writes about a repository it is working in.
		{
			name: "an agent describing a repo without a manifest",
			raw:  "I looked at the project and it has no package.json, so I could not run the build",
			want: ReasonAgentUnknown,
		},
		{
			name: "an agent describing a malformed manifest",
			raw:  "the checkout has invalid package.json and npm refused to install",
			want: ReasonAgentUnknown,
		},
		{
			name: "an agent describing a lockfile problem",
			raw:  "npm ci stopped on an integrity mismatch in the lockfile",
			want: ReasonAgentUnknown,
		},
		{
			name: "an agent talking about plugins generally",
			raw:  "the plugin worked fine but the test harness did not",
			want: ReasonAgentUnknown,
		},

		// Near misses on the loader wording.
		{"generic load failure", "failed to load the file", ReasonAgentUnknown},
		{"generic import failure", "could not import the module", ReasonAgentUnknown},

		// Both of these matched an earlier draft of the witness. They are the
		// reason each alternative now carries the rest of the adapter's
		// sentence rather than its most memorable phrase.
		{
			name: "an npm package whose name happens to contain \"plugin\"",
			raw:  "the build failed because plugin eslint-plugin-import declares an incompatible peer dependency",
			want: ReasonAgentUnknown,
		},
		{
			name: "a different runner failing to fetch its own plugin",
			raw:  "opencode failed to fetch plugin @acme/opencode-tools: connection reset by peer",
			want: ReasonAgentUnknown,
		},
		{
			// The same shape over a witness rule 7 does recognise. Misrouting
			// this one costs more than a label: provider_network is the only
			// agent-side reason on the auto-retry allowlist, so claiming it
			// would take away the retry it exists to trigger.
			name: "a different runner's plugin fetch, over a transient cut",
			raw:  "opencode failed to fetch plugin @acme/opencode-tools: connection closed",
			want: ReasonAgentProviderNetwork,
		},
	}
	for _, c := range cases {
		name := c.name
		if name == "" {
			name = c.raw
		}
		t.Run(name, func(t *testing.T) {
			if got := Classify(c.raw); got != c.want {
				t.Errorf("Classify(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// TestNormalizeDaemonReason is the mixed-version regression for MUL-5370.
//
// The daemon-side fix labels a failed skill-bundle download structurally, but
// installed daemons upgrade on their own cadence. An un-upgraded daemon reports
// a NON-EMPTY catchall, which FailTask's "classify only when empty" guard
// deliberately preserves — so without this normalisation the fix would reach
// only hosts that happened to update: no auto-retry (the catchall is not on the
// retry allowlist) and generic chat copy, on exactly the hosts most likely to
// be hitting the bug.
func TestNormalizeDaemonReason(t *testing.T) {
	t.Parallel()

	const legacyErr = "resolve skill bundles: context deadline exceeded"

	cases := []struct {
		name   string
		reason string
		raw    string
		want   Reason
	}{
		{
			name:   "old daemon catchall is upgraded",
			reason: string(ReasonAgentUnknown),
			raw:    legacyErr,
			want:   ReasonSkillBundleUnavailable,
		},
		{
			// A daemon new enough to classify the deadline as network, but not
			// new enough to know the failure was a skill bundle.
			name:   "old daemon network guess is upgraded",
			reason: string(ReasonAgentProviderNetwork),
			raw:    legacyErr,
			want:   ReasonSkillBundleUnavailable,
		},
		{
			name:   "pre-MUL-1949 coarse reason is upgraded",
			reason: "agent_error",
			raw:    legacyErr,
			want:   ReasonSkillBundleUnavailable,
		},
		{
			name:   "leading whitespace does not defeat the witness",
			reason: string(ReasonAgentUnknown),
			raw:    "  " + legacyErr,
			want:   ReasonSkillBundleUnavailable,
		},
		{
			// A current daemon already sends the right reason and a different
			// error string; nothing to do.
			name:   "current daemon reason passes through",
			reason: string(ReasonSkillBundleUnavailable),
			raw:    `skill bundle unavailable: skill "x" (id=1, 10 bytes) after 30s: context deadline exceeded`,
			want:   ReasonSkillBundleUnavailable,
		},
		{
			// The witness is a prefix, not a substring: an agent that merely
			// mentions the old wrapper in its output must not be relabelled.
			name:   "prefix only, not substring",
			reason: string(ReasonAgentUnknown),
			raw:    "the agent said it could not resolve skill bundles: and then gave up",
			want:   ReasonAgentUnknown,
		},
		{
			name:   "unrelated reason with the witness is left alone",
			reason: string(ReasonAgentProviderAuthOrAccess),
			raw:    legacyErr,
			want:   ReasonAgentProviderAuthOrAccess,
		},
		{
			name:   "catchall without the witness is left alone",
			reason: string(ReasonAgentUnknown),
			raw:    "claude exited with error: exit status 1",
			want:   ReasonAgentUnknown,
		},
		{
			name:   "empty reason is left alone for the caller's classifier",
			reason: "",
			raw:    legacyErr,
			want:   Reason(""),
		},

		// --- GH #6360: response-side context overflow. An un-upgraded daemon
		// classifies the wordings below as the catchall, which is on no resume
		// blacklist — so without this the over-full session stays pinned and
		// every later comment on the issue replays the same overflow.
		{
			name:   "old daemon catchall on the claude code wording is upgraded",
			reason: string(ReasonAgentUnknown),
			raw:    "API Error: The model has reached its context window limit.",
			want:   ReasonAgentContextOverflow,
		},
		{
			name:   "old daemon catchall on the raw stop reason is upgraded",
			reason: string(ReasonAgentUnknown),
			raw:    `{"stop_reason":"model_context_window_exceeded"}`,
			want:   ReasonAgentContextOverflow,
		},
		{
			name:   "pre-MUL-1949 coarse reason on the overflow is upgraded",
			reason: "agent_error",
			raw:    "API Error: The model has reached its context window limit.",
			want:   ReasonAgentContextOverflow,
		},
		{
			// The witness is matched case-insensitively, like Classify's.
			name:   "witness casing does not defeat the upgrade",
			reason: string(ReasonAgentUnknown),
			raw:    "API ERROR: THE MODEL HAS REACHED ITS CONTEXT WINDOW LIMIT.",
			want:   ReasonAgentContextOverflow,
		},
		{
			// A current daemon already classified it; nothing to do.
			name:   "current daemon overflow reason passes through",
			reason: string(ReasonAgentContextOverflow),
			raw:    "API Error: The model has reached its context window limit.",
			want:   ReasonAgentContextOverflow,
		},
		{
			// A refined reason means the old daemon matched an earlier rule on
			// this same text. That is a stronger statement about what ended the
			// run than the witness is, so it is left alone.
			name:   "refined reason with the overflow witness is left alone",
			reason: string(ReasonAgentProcessFailure),
			raw:    "claude exited with error: exit status 1: The model has reached its context window limit.",
			want:   ReasonAgentProcessFailure,
		},
		{
			name:   "catchall without an overflow witness is left alone",
			reason: string(ReasonAgentUnknown),
			raw:    "API Error: the model is overloaded",
			want:   ReasonAgentUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeDaemonReason(tc.reason, tc.raw); got != tc.want {
				t.Errorf("NormalizeDaemonReason(%q, %q) = %q, want %q", tc.reason, tc.raw, got, tc.want)
			}
		})
	}
}

// TestNormalizeDaemonReason_UpgradedReasonIsRetryable pins the property that
// actually matters to the user: the normalised reason must be one the server
// retries. If someone later drops skill_bundle_unavailable from
// internal/service/task.go's retryableReasons, the label survives but the
// self-healing this PR is for silently disappears.
func TestNormalizeDaemonReason_UpgradedReasonIsPlatformSide(t *testing.T) {
	t.Parallel()

	got := NormalizeDaemonReason(string(ReasonAgentUnknown), "resolve skill bundles: context deadline exceeded")
	if got.IsAgentError() {
		t.Errorf("%q must be platform-side: the agent process never started", got)
	}
}

// TestNormalizeDaemonReasonUpgradesADshPluginFailure covers the mixed-version
// gap for plugin preparation.
//
// The daemon classifies the failure itself and sends a confident
// agent_error.*, so FailTask's "classify when empty" branch never runs — a
// server-only deploy would keep persisting the wrong label until every host
// updated. That matters more here than for a vague bucket: the label is what
// tells the operator to look at a plugin instead of at their model account.
func TestNormalizeDaemonReasonUpgradesADshPluginFailure(t *testing.T) {
	t.Parallel()

	fetchErr := "failed to fetch plugin dsh-mcp-lens-uploaded: HTTP Error 403: Forbidden"
	mountErr := "plugin tree failed to load: failed to import loader entry mcp-lens"

	cases := []struct {
		name   string
		reason string
		raw    string
		want   Reason
	}{
		{
			name:   "the 403 an old daemon read as a provider auth failure",
			reason: string(ReasonAgentProviderAuthOrAccess),
			raw:    fetchErr,
			want:   ReasonDshPluginUnavailable,
		},
		{
			name:   "the stack trace an old daemon read as a rate limit",
			reason: string(ReasonAgentProviderCapacityOrRateLimit),
			raw:    mountErr,
			want:   ReasonDshPluginUnavailable,
		},
		{
			name:   "the catchall",
			reason: string(ReasonAgentUnknown),
			raw:    mountErr,
			want:   ReasonDshPluginUnavailable,
		},
		{
			name:   "the pre-MUL-1949 coarse reason",
			reason: "agent_error",
			raw:    fetchErr,
			want:   ReasonDshPluginUnavailable,
		},
		{
			name:   "a current daemon already sends the right reason",
			reason: string(ReasonDshPluginUnavailable),
			raw:    fetchErr,
			want:   ReasonDshPluginUnavailable,
		},
		{
			// The daemon knew something the text does not say. A witness
			// somewhere in the blob is weaker evidence than that.
			name:   "a reason no digit-scanning rule could have produced is kept",
			reason: string(ReasonAgentTimeout),
			raw:    mountErr,
			want:   ReasonAgentTimeout,
		},
		{
			name:   "no witness, nothing to upgrade",
			reason: string(ReasonAgentProviderAuthOrAccess),
			raw:    "401 Unauthorized",
			want:   ReasonAgentProviderAuthOrAccess,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeDaemonReason(c.reason, c.raw); got != c.want {
				t.Errorf("NormalizeDaemonReason(%q, %q) = %q, want %q",
					c.reason, c.raw, got, c.want)
			}
		})
	}
}
