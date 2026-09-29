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
// experiment isolates the pruning: while it is on, each Coordinator job is
// assigned once, by a stable hash of its id, to a group that prunes or one
// that keeps the expanded schema; every other request parameter is the same
// in both groups. It never applies when the performance switch is off.
const (
	finishSchemaPruned   = "pruned"
	finishSchemaExpanded = "expanded"
)

// FinishSchemaExperiment is the experiment configuration a decision samples,
// with the runtime configuration identity it came from.
type FinishSchemaExperiment struct {
	Enabled          bool
	Salt             string
	ConfigSHA256     string
	ConfigGeneration uint64
}

// finishSchemaAssignment is the group of one decision, fixed when the
// decision samples its configuration.
type finishSchemaAssignment struct {
	active           bool
	arm              string
	reason           string
	jobID            string
	configSHA256     string
	configGeneration uint64
}

// assignFinishSchemaArm decides the experiment group of a decision. Only an
// inbound decision of a Coordinator job, under an enabled performance switch
// (finishRecovery), takes part; any other decision keeps the switch's normal
// behavior, and a job without a usable id is recorded as excluded.
func (c *Coordinator) assignFinishSchemaArm(ctx context.Context, turn Turn, finishRecovery bool) finishSchemaAssignment {
	if c == nil || c.FinishSchemaExperimentProvider == nil || !finishRecovery {
		return finishSchemaAssignment{}
	}
	experiment := c.FinishSchemaExperimentProvider(turn.AgentID)
	if !experiment.Enabled {
		return finishSchemaAssignment{}
	}
	assignment := finishSchemaAssignment{active: true, configSHA256: experiment.ConfigSHA256, configGeneration: experiment.ConfigGeneration}
	if turn.Loop != "" && turn.Loop != LoopInbound {
		assignment.reason = "excluded_loop"
		return assignment
	}
	jobID := TraceIDFromContext(ctx)
	if parsed, err := uuid.Parse(jobID); err != nil || parsed == uuid.Nil {
		assignment.reason = "excluded_no_job_id"
		return assignment
	}
	assignment.jobID = jobID
	assignment.arm = finishSchemaArm(experiment.Salt, jobID)
	assignment.reason = "included"
	return assignment
}

// finishSchemaArm is a stable function of the salt and the job id, so every
// claim, park, retry and replica of a job lands in the same group.
func finishSchemaArm(salt, jobID string) string {
	sum := sha256.Sum256([]byte(salt + "\x00" + strings.ToLower(jobID)))
	if binary.BigEndian.Uint64(sum[:8])%2 == 0 {
		return finishSchemaPruned
	}
	return finishSchemaExpanded
}

// logFinishSchemaAssignment records a decision in the experiment before any
// model request, so decisions that fail or never reach a request stay in
// the denominator.
func (c *Coordinator) logFinishSchemaAssignment(ctx context.Context, turn Turn) {
	a := c.finishSchemaAssignment
	if !a.active || hostQuiet(ctx) {
		return
	}
	slog.Info("inbound coordinator finish schema experiment assigned", append(coordinatorLogIndex(turn),
		"event", "inbound_coordinator_finish_schema_assigned",
		"arm", a.arm, "reason", a.reason, "job_id", a.jobID,
		"build", c.BuildID, "config_sha256", a.configSHA256, "config_generation", a.configGeneration,
		"model", c.configuredModel(), "finish_recovery", c.finishRecoveryEnabled())...)
}

// logFinishSchemaRequest records, before a model request is sent, the finish
// schema that request actually carries.
func (c *Coordinator) logFinishSchemaRequest(turn Turn, round int, params openai.ChatCompletionNewParams) {
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
		"arm", a.arm, "job_id", a.jobID, "round", round, "model", string(params.Model),
		"finish_schema_sha", hash, "finish_schema_bytes", size, "tools", len(params.Tools),
		"config_sha256", a.configSHA256)...)
}
