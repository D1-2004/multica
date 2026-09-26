package dshprofile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/google/uuid"
)

const MaxBuildArchiveBytes int64 = 2*1024*1024*1024 + 64*1024*1024

var ErrBuildClaimLost = errors.New("DSH plugin build claim changed")
var ErrNoBuildJob = errors.New("no due DSH plugin build")
var ErrBuildSourceInvalid = errors.New("DSH plugin source archive cannot be recovered with its recorded identity")
var workerFailureCode = regexp.MustCompile(`^(source_download|dependency_build|artifact_export|artifact_upload)_(failed|http_[45][0-9]{2})$`)

var workerSandboxID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,160}$`)

// BuildArtifact is a verified transfer receipt, never a URL or credential.
type BuildArtifact struct {
	BuildDigest       string `json:"build_digest"`
	ArchiveSHA256     string `json:"archive_sha256"`
	ArchiveSize       int64  `json:"archive_size"`
	RuntimeLockSHA256 string `json:"runtime_lock_sha256"`
}

func (a BuildArtifact) Valid() bool {
	return digestPattern.MatchString(a.BuildDigest) && digestPattern.MatchString(a.ArchiveSHA256) &&
		digestPattern.MatchString(a.RuntimeLockSHA256) && a.ArchiveSize > 0 && a.ArchiveSize <= MaxBuildArchiveBytes
}

// BuildJob contains only package provenance and execution identity. Employee
// configuration, model/DWS credentials and presigned URLs never enter the ledger.
type BuildJob struct {
	WorkspaceID uuid.UUID
	BuildID     uuid.UUID
	BuildKey    string
	TemplateID  string
	Plugin      SourcePlugin
	State       string
	Phase       string
	Intent      uuid.UUID
	Scope       string
	SandboxID   string
	ArtifactKey string
	Artifact    BuildArtifact
	ErrorCode   string
	ClaimID     uuid.UUID
	StartedAt   time.Time
	QueuedAt    time.Time
}

func (j BuildJob) objectKey() string {
	return fmt.Sprintf("dsh-plugin-builds/%s/%s/%s/tree.tgz", j.WorkspaceID, j.BuildKey, j.Intent)
}

func (j BuildJob) valid() bool {
	if j.WorkspaceID == uuid.Nil || j.BuildID == uuid.Nil || j.Plugin.ID == uuid.Nil || j.ClaimID == uuid.Nil ||
		!workerSandboxID.MatchString(j.TemplateID) || j.BuildKey != BuildKey(j.TemplateID, j.Plugin) {
		return false
	}
	// Reuse source boundary validation without admitting employee configuration.
	source := Source{TemplateID: j.TemplateID, Plugins: []SourcePlugin{j.Plugin}}
	if _, _, err := EncodeSource(source); err != nil || len(j.Plugin.Config) != 0 || j.Plugin.ConfigRevision != 0 || j.Plugin.RowID != "" {
		return false
	}
	if j.Phase == "done" && j.State == "failed" && j.Intent == uuid.Nil {
		return j.SandboxID == "" && j.Scope == "" && j.ArtifactKey == "" &&
			(j.ErrorCode == "source_archive_invalid" || j.ErrorCode == "build_prerequisites_timeout")
	}
	if j.Phase != "queued" && (j.Intent == uuid.Nil || j.Scope == "" || j.ArtifactKey != j.objectKey() || j.StartedAt.IsZero()) {
		return false
	}
	if j.Phase != "queued" && j.Phase != "creating" && !workerSandboxID.MatchString(j.SandboxID) {
		return false
	}
	return true
}

type BuildLedger interface {
	Claim(context.Context) (BuildJob, error)
	Save(context.Context, BuildJob, BuildJob) error
	Release(context.Context, BuildJob, time.Duration) error
}

type BuildObservation struct {
	ErrorCode string
	State     string // running, succeeded, failed, absent
	Artifact  BuildArtifact
}

// BuildDriver implementations must bind every operation to the persisted
// intent/template/scope. Start is remotely idempotent for that exact intent;
// Create is never retried. VerifyArtifact reads stored bytes, not upload status.
type BuildDriver interface {
	Scope() string
	Preflight(context.Context, BuildJob) error
	Create(context.Context, BuildJob) (string, error)
	FindCreated(context.Context, BuildJob) (string, error)
	Start(context.Context, BuildJob) error
	Inspect(context.Context, BuildJob) (BuildObservation, error)
	VerifyArtifact(context.Context, BuildJob) error
	DestroyAndConfirmAbsent(context.Context, BuildJob) error
}

type BuildWorker struct {
	OnReady func(context.Context, uuid.UUID)

	Ledger BuildLedger
	Driver BuildDriver
	Now    func() time.Time
}

func (w BuildWorker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Step advances one durable boundary. A lost SQL receipt never licenses repeating
// a create; a replacement claimant starts at reconciliation of the saved phase.
func (w BuildWorker) Step(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if w.Ledger == nil || w.Driver == nil {
		return errors.New("DSH build worker unavailable")
	}
	job, err := w.Ledger.Claim(ctx)
	if err != nil {
		return err
	}
	delay := 5 * time.Second
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = w.Ledger.Release(cleanup, job, delay)
	}()
	if !job.valid() || (job.Phase != "queued" && job.Scope != w.Driver.Scope()) {
		return errors.New("DSH build execution identity mismatch")
	}
	save := func(next BuildJob) error {
		if err := w.Ledger.Save(ctx, job, next); err != nil {
			return err
		}
		becameReady := job.State != "ready" && next.State == "ready"
		job = next
		if becameReady && w.OnReady != nil {
			w.OnReady(ctx, job.WorkspaceID)
		}
		slog.InfoContext(ctx, "dsh_plugin_build_transition",
			"workspace_id", job.WorkspaceID.String(), "build_id", job.BuildID.String(),
			"phase", job.Phase, "state", job.State, "template_id", job.TemplateID,
			"sandbox_id", job.SandboxID, "build_digest", job.Artifact.BuildDigest,
			"archive_sha256", job.Artifact.ArchiveSHA256, "archive_size", job.Artifact.ArchiveSize,
			"error_code", job.ErrorCode)
		return nil
	}
	fail := func(code string) error {
		next := job
		next.Phase, next.State, next.ErrorCode = "cleanup", "failed", code
		return save(next)
	}
	switch job.Phase {
	case "queued":
		if err := w.Driver.Preflight(ctx, job); err != nil {
			code := ""
			if errors.Is(err, ErrBuildSourceInvalid) {
				code = "source_archive_invalid"
			} else if !job.QueuedAt.IsZero() && w.now().Sub(job.QueuedAt) > 10*time.Minute {
				code = "build_prerequisites_timeout"
			}
			if code != "" {
				next := job
				next.Phase, next.State, next.ErrorCode = "done", "failed", code
				return save(next)
			}
			delay = time.Minute
			return errors.New("DSH build prerequisites unavailable")
		}
		next := job
		next.Phase, next.Intent, next.Scope, next.StartedAt = "creating", uuid.New(), w.Driver.Scope(), w.now()
		next.ArtifactKey = next.objectKey()
		if next.Scope == "" {
			return errors.New("DSH build provider scope unavailable")
		}
		if err := save(next); err != nil {
			return err
		}
		id, err := w.Driver.Create(ctx, job)
		if err != nil || !workerSandboxID.MatchString(id) {
			return errors.New("DSH build create outcome unconfirmed")
		}
		next = job
		next.SandboxID, next.Phase = id, "starting"
		return save(next)
	case "creating":
		id, err := w.Driver.FindCreated(ctx, job)
		if err != nil || !workerSandboxID.MatchString(id) {
			return errors.New("DSH build create reconciliation pending")
		}
		next := job
		next.SandboxID, next.Phase = id, "starting"
		return save(next)
	case "starting":
		if w.now().Sub(job.StartedAt) > 30*time.Minute {
			return fail("build_timeout")
		}
		if err := w.Driver.Start(ctx, job); err != nil {
			return errors.New("DSH build start receipt unconfirmed")
		}
		next := job
		next.Phase = "building"
		return save(next)
	case "building":
		if w.now().Sub(job.StartedAt) > 30*time.Minute {
			return fail("build_timeout")
		}
		observed, err := w.Driver.Inspect(ctx, job)
		if err != nil {
			return errors.New("DSH build status unavailable")
		}
		switch observed.State {
		case "succeeded":
			if !observed.Artifact.Valid() {
				return fail("invalid_artifact_receipt")
			}
			next := job
			next.Artifact, next.Phase = observed.Artifact, "publishing"
			return save(next)
		case "failed", "absent":
			code := "build_failed"
			if workerFailureCode.MatchString(observed.ErrorCode) {
				code = observed.ErrorCode
			}
			return fail(code)
		case "running":
			return nil
		default:
			return errors.New("invalid DSH build execution status")
		}
	case "publishing":
		if !job.Artifact.Valid() {
			return fail("invalid_artifact_receipt")
		}
		if err := w.Driver.VerifyArtifact(ctx, job); err != nil {
			if w.now().Sub(job.StartedAt) > 35*time.Minute {
				return fail("artifact_verification_failed")
			}
			return errors.New("DSH build artifact verification pending")
		}
		next := job
		next.Phase, next.State = "cleanup", "ready"
		return save(next)
	case "cleanup":
		if err := w.Driver.DestroyAndConfirmAbsent(ctx, job); err != nil {
			return errors.New("DSH build sandbox cleanup unconfirmed")
		}
		next := job
		next.Phase = "done"
		return save(next)
	default:
		return errors.New("invalid DSH build worker phase")
	}
}
