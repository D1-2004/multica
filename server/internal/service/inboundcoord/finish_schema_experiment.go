package inboundcoord

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/openai/openai-go/v3"
)

// The finish recovery of the performance switch prunes the expanded finish
// schema (about 9.7k to 3k tokens per first request in production) together
// with a larger output budget and serialization repair. The finish schema
// experiment isolates the pruning: while it splits, each Coordinator job is
// assigned once, by a stable hash of its id, to a group that prunes or one
// that keeps the expanded schema; every other request parameter is the same
// in both groups. Mode "expanded" is the stop-loss that keeps the expanded
// schema for every decision while the performance switch stays on. It never
// applies when the performance switch is off.
const (
	finishSchemaPruned   = "pruned"
	finishSchemaExpanded = "expanded"

	finishSchemaModeExpanded = "expanded"
)

// DecisionConfig is one read of the runtime configuration. A decision takes
// its model, performance switch, finish schema experiment and configuration
// identity from the same read, so a hot update cannot mix two versions.
type DecisionConfig struct {
	Model            string
	Performance      bool
	FinishSchema     FinishSchemaExperiment
	ConfigSHA256     string
	ConfigGeneration uint64
}

// FinishSchemaExperiment is the experiment part of a DecisionConfig.
type FinishSchemaExperiment struct {
	Enabled bool
	Mode    string
	Salt    string
}

func (e FinishSchemaExperiment) forcesExpanded() bool {
	return e.Enabled && e.Mode == finishSchemaModeExpanded
}

// FinishSchemaRecord is the group a job took in its first split decision,
// persisted with the job so later claims of the same job can tell whether
// the experiment configuration changed in between.
type FinishSchemaRecord struct {
	Arm              string `json:"arm"`
	SaltDigest       string `json:"salt_digest"`
	ConfigSHA256     string `json:"config_sha256"`
	ConfigGeneration uint64 `json:"config_generation"`
}

type finishSchemaRecordKey struct{}
type finishSchemaRecordStore struct {
	saved *FinishSchemaRecord
	save  func(FinishSchemaRecord) error
}

// ContextWithFinishSchemaRecord is supplied only by the durable job Host
// with the record read from the claimed job and a lease-guarded save.
func ContextWithFinishSchemaRecord(ctx context.Context, saved *FinishSchemaRecord, save func(FinishSchemaRecord) error) context.Context {
	return context.WithValue(ctx, finishSchemaRecordKey{}, finishSchemaRecordStore{saved: saved, save: save})
}

func finishSchemaRecordFrom(ctx context.Context) finishSchemaRecordStore {
	store, _ := ctx.Value(finishSchemaRecordKey{}).(finishSchemaRecordStore)
	return store
}

// finishSchemaAssignment is the group of one decision, fixed when the
// decision samples its configuration.
type finishSchemaAssignment struct {
	active           bool
	arm              string
	reason           string
	mode             string
	jobID            string
	saltDigest       string
	record           string
	configSHA256     string
	configGeneration uint64
}

// assignFinishSchemaArm decides the experiment group of a decision from one
// DecisionConfig. Only an inbound decision of a Coordinator job, under the
// performance switch, takes part in the split. Decisions that cannot be
// compared are recorded with the reason they are excluded.
func assignFinishSchemaArm(ctx context.Context, turn Turn, cfg DecisionConfig) finishSchemaAssignment {
	store := finishSchemaRecordFrom(ctx)
	experiment := cfg.FinishSchema
	a := finishSchemaAssignment{active: true, mode: experiment.Mode, configSHA256: cfg.ConfigSHA256, configGeneration: cfg.ConfigGeneration, record: "none"}
	if jobID := TraceIDFromContext(ctx); isJobID(jobID) {
		a.jobID = jobID
	}
	if store.saved != nil {
		a.record = "reused"
	}
	if !cfg.Performance || !experiment.Enabled {
		if store.saved == nil {
			return finishSchemaAssignment{}
		}
		// The job was split under an earlier configuration and now takes the
		// current one; it is kept out of the comparison.
		a.reason = "excluded_config_changed"
		return a
	}
	if _, restored := RestoredPlan(ctx); restored {
		a.reason = "excluded_checkpoint"
		return a
	}
	if experiment.forcesExpanded() {
		a.arm, a.reason = finishSchemaExpanded, "forced_expanded"
		return a
	}
	if turn.Loop != "" && turn.Loop != LoopInbound {
		a.reason = "excluded_loop"
		return a
	}
	if a.jobID == "" {
		a.reason = "excluded_no_job_id"
		return a
	}
	a.saltDigest = finishSchemaSaltDigest(experiment.Salt)
	a.arm = finishSchemaArm(experiment.Salt, a.jobID)
	a.reason = "included"
	if saved := store.saved; saved != nil && saved.SaltDigest != a.saltDigest {
		a.reason = "excluded_config_changed"
	}
	return a
}

func isJobID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil
}

// finishSchemaArm is a stable function of the salt and the job id, so every
// claim, park, retry and replica of a job lands in the same group while the
// salt is unchanged.
func finishSchemaArm(salt, jobID string) string {
	sum := sha256.Sum256([]byte(salt + "\x00" + strings.ToLower(jobID)))
	if binary.BigEndian.Uint64(sum[:8])%2 == 0 {
		return finishSchemaPruned
	}
	return finishSchemaExpanded
}

func finishSchemaSaltDigest(salt string) string {
	sum := sha256.Sum256([]byte(salt))
	return hex.EncodeToString(sum[:8])
}

// recordFinishSchemaAssignment logs a decision in the experiment before any
// model request, so decisions that fail or never reach a request stay in
// the denominator, and persists the first split group of the job.
func (c *Coordinator) recordFinishSchemaAssignment(ctx context.Context, turn Turn, routeError bool) {
	a := &c.finishSchemaAssignment
	if !a.active {
		return
	}
	if store := finishSchemaRecordFrom(ctx); a.reason == "included" && store.saved == nil && store.save != nil {
		a.record = "saved"
		if err := store.save(FinishSchemaRecord{Arm: a.arm, SaltDigest: a.saltDigest, ConfigSHA256: a.configSHA256, ConfigGeneration: a.configGeneration}); err != nil {
			a.record = "save_failed"
		}
	}
	if hostQuiet(ctx) {
		return
	}
	slog.Info("inbound coordinator finish schema experiment assigned", append(coordinatorLogIndex(turn),
		"event", "inbound_coordinator_finish_schema_assigned",
		"arm", a.arm, "reason", a.reason, "mode", a.mode, "job_id", a.jobID, "record", a.record,
		"route_error", routeError, "build", c.BuildID,
		"config_sha256", a.configSHA256, "config_generation", a.configGeneration,
		"model", c.configuredModel(), "finish_recovery", c.finishRecoveryEnabled())...)
}

// logFinishSchemaRequest records, before a model request carrying the finish
// tool is sent, the finish schema that request actually carries. kind is
// "route" for a loop round and "finish_repair" for a serialization repair.
func (c *Coordinator) logFinishSchemaRequest(turn Turn, kind string, round int, params openai.ChatCompletionNewParams) {
	a := c.finishSchemaAssignment
	if !a.active || a.arm == "" {
		return
	}
	hash, size := "", 0
	for _, tool := range params.Tools {
		if tool.OfFunction == nil || tool.OfFunction.Function.Name != toolFinish {
			continue
		}
		raw, err := json.Marshal(tool)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(raw)
		hash, size = hex.EncodeToString(sum[:6]), len(raw)
	}
	slog.Info("inbound coordinator finish schema experiment request", append(coordinatorLogIndex(turn),
		"event", "inbound_coordinator_finish_schema_request",
		"arm", a.arm, "kind", kind, "job_id", a.jobID, "round", round, "model", string(params.Model),
		"finish_schema_sha", hash, "finish_schema_bytes", size, "tools", len(params.Tools),
		"config_sha256", a.configSHA256)...)
}
