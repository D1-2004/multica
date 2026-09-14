package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshprofile"
	"github.com/multica-ai/multica/server/internal/storage"
)

const DSHPluginBuildCapability = "dsh_plugin_build_worker_v1"

type dshBuildDriver struct {
	launcher *FCE2BLauncher
	provider *dshhost.FCBuildProvider
	objects  storage.Storage
	get      storage.Presigner
	put      storage.PutPresigner
}

func newDSHBuildDriver(l *FCE2BLauncher, objects storage.Storage) (*dshBuildDriver, error) {
	get, canGet := objects.(storage.Presigner)
	put, canPut := objects.(storage.PutPresigner)
	if l == nil || !l.Config.Enabled || l.Runner == nil || !canGet || !canPut {
		return nil, errors.New("DSH cloud builds require FC and signed object transfers")
	}
	provider, err := dshhost.NewFCBuildProvider(dshhost.FCConfig{APIURL: l.Config.APIURL, APIKey: l.Config.APIKey, TimeoutSeconds: 1800})
	if err != nil {
		return nil, err
	}
	return &dshBuildDriver{l, provider, objects, get, put}, nil
}

func buildIdentity(job dshprofile.BuildJob) dshhost.FCBuildIdentity {
	return dshhost.FCBuildIdentity{WorkspaceID: job.WorkspaceID, BuildID: job.BuildID, Intent: job.Intent, BuildKey: job.BuildKey, TemplateID: job.TemplateID}
}

func (*dshBuildDriver) frozenRequest(job dshprofile.BuildJob) map[string]any {
	return map[string]any{
		"identity": map[string]string{
			"workspace_id": job.WorkspaceID.String(), "build_id": job.BuildID.String(), "intent": job.Intent.String(),
			"build_key": job.BuildKey, "template_id": job.TemplateID, "artifact_key": job.ArtifactKey,
		},
		"plugin": map[string]string{"package_name": job.Plugin.PackageName, "version": job.Plugin.Version, "integrity": job.Plugin.Integrity},
	}
}

func (d *dshBuildDriver) Scope() string { return d.provider.Scope() }

func (d *dshBuildDriver) Preflight(ctx context.Context, job dshprofile.BuildJob) error {
	if !strings.HasPrefix(job.Plugin.ArtifactKey, "dsh-plugins/"+job.WorkspaceID.String()+"/") {
		return errors.New("DSH source archive is not scoped to the build workspace")
	}
	templates, err := ListFCE2BTemplates(ctx, d.launcher.Config, d.launcher.Runner)
	if err != nil {
		return errors.New("DSH build template catalog unavailable")
	}
	if !dshBuildTemplateReady(templates, job.TemplateID) {
		return errors.New("DSH build template is not ready for this provider")
	}
	reader, err := d.objects.GetReader(ctx, job.Plugin.ArtifactKey)
	if err != nil {
		return errors.New("DSH source archive unavailable")
	}
	return reader.Close()
}

func dshBuildTemplateReady(templates []FCE2BTemplate, id string) bool {
	// Catalog capabilities describe provider admission, not installed helpers.
	// Start checks the fixed helper's protocol before granting object access.
	for _, template := range templates {
		if template.ID == id && IsFCE2BTemplateReady(template) && FCE2BTemplateSupportsProvider(template, "dsh") {
			return true
		}
	}
	return false
}

func (d *dshBuildDriver) Create(ctx context.Context, job dshprofile.BuildJob) (string, error) {
	return d.provider.Create(ctx, buildIdentity(job))
}
func (d *dshBuildDriver) FindCreated(ctx context.Context, job dshprofile.BuildJob) (string, error) {
	return d.provider.FindCreated(ctx, buildIdentity(job))
}

type dshBuildControlReply struct {
	State             string                   `json:"state"`
	IdentityDigest    string                   `json:"identity_digest"`
	Artifact          dshprofile.BuildArtifact `json:"artifact"`
	ErrorCode         string                   `json:"error_code"`
	Protocol          string                   `json:"protocol"`
	RuntimeLockSHA256 string                   `json:"runtime_lock_sha256"`
}

func (d *dshBuildDriver) control(ctx context.Context, job dshprofile.BuildJob, operation string, transfer bool) (dshBuildControlReply, error) {
	frozen := d.frozenRequest(job)
	encoded, err := json.Marshal(frozen)
	if err != nil {
		return dshBuildControlReply{}, errors.New("invalid DSH build identity")
	}
	digest := sha256.Sum256(encoded)
	request := frozen
	if operation == "capabilities" {
		request = map[string]any{}
	}
	request["operation"] = operation
	if transfer {
		source, err := d.get.PresignGet(ctx, job.Plugin.ArtifactKey, 30*time.Minute)
		if err != nil {
			return dshBuildControlReply{}, errors.New("DSH source transfer grant unavailable")
		}
		target, err := d.put.PresignPut(ctx, job.ArtifactKey, "application/gzip", 30*time.Minute)
		if err != nil {
			return dshBuildControlReply{}, errors.New("DSH artifact upload grant unavailable")
		}
		request["source_url"], request["upload_url"] = source, target
	}
	encoded, err = json.Marshal(request)
	if err != nil || len(encoded) > 65536 {
		return dshBuildControlReply{}, errors.New("invalid DSH build control request")
	}
	out, err := d.launcher.runE2BCommandWithTimeout(ctx, 30*time.Second, []string{
		"sandbox", "exec", "--user", "user", "-e", "LD_PRELOAD=", "-e", "LD_LIBRARY_PATH=",
		"-e", "PYTHONPATH=", "-e", "PYTHONHOME=", "-e", "MULTICA_DSH_BUILD_REQUEST=" + string(encoded),
		job.SandboxID, "--", "/opt/task-python/bin/python3", "-E", "-s", "/opt/multica-dsh/multica_dsh_build_worker.py",
	})
	if err != nil || len(out) > 65536 {
		// CLI errors can echo command arguments containing signed URLs.
		return dshBuildControlReply{}, errors.New("DSH build control receipt unavailable")
	}
	var reply dshBuildControlReply
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &reply); err != nil {
		return reply, errors.New("invalid DSH build control receipt")
	}
	if operation != "capabilities" && reply.State != "absent" && reply.IdentityDigest != hex.EncodeToString(digest[:]) {
		return reply, errors.New("DSH build receipt identity mismatch")
	}
	return reply, nil
}

func (d *dshBuildDriver) Start(ctx context.Context, job dshprofile.BuildJob) error {
	exists, state, err := d.provider.Inspect(ctx, buildIdentity(job), job.SandboxID)
	if err != nil || !exists || state != "running" {
		return errors.New("DSH build sandbox not ready")
	}
	capability, err := d.control(ctx, job, "capabilities", false)
	if err != nil || capability.Protocol != DSHPluginBuildCapability || len(capability.RuntimeLockSHA256) != 64 || !isLowerHex(capability.RuntimeLockSHA256) {
		return errors.New("DSH build execution protocol unavailable")
	}
	reply, err := d.control(ctx, job, "start", true)
	if err != nil {
		return err
	}
	if reply.State != "running" && reply.State != "succeeded" && reply.State != "failed" {
		return errors.New("invalid DSH build start status")
	}
	return nil
}

func (d *dshBuildDriver) Inspect(ctx context.Context, job dshprofile.BuildJob) (dshprofile.BuildObservation, error) {
	exists, state, err := d.provider.Inspect(ctx, buildIdentity(job), job.SandboxID)
	if err != nil {
		return dshprofile.BuildObservation{}, err
	}
	if !exists {
		return dshprofile.BuildObservation{State: "absent"}, nil
	}
	if state != "running" {
		return dshprofile.BuildObservation{}, errors.New("DSH build sandbox is not running")
	}
	reply, err := d.control(ctx, job, "status", false)
	if err != nil {
		return dshprofile.BuildObservation{}, err
	}
	return dshprofile.BuildObservation{State: reply.State, Artifact: reply.Artifact, ErrorCode: reply.ErrorCode}, nil
}

func (d *dshBuildDriver) VerifyArtifact(ctx context.Context, job dshprofile.BuildJob) error {
	return dshprofile.VerifyBuildObject(ctx, d.objects, job)
}

func (d *dshBuildDriver) DestroyAndConfirmAbsent(ctx context.Context, job dshprofile.BuildJob) error {
	if err := d.provider.DestroyAndConfirmAbsent(ctx, buildIdentity(job), job.SandboxID); err != nil {
		return err
	}
	if job.State == "failed" {
		// The object key was saved before granting PUT, so even an unknown
		// upload response has a bounded cleanup identity.
		return d.objects.DeleteObject(ctx, job.ArtifactKey)
	}
	return nil
}

// RunDSHBuildWorker reconciles shared PostgreSQL intents. Configuration is
// frozen per step; no request-owned goroutine or in-memory job owns progress.
func (l *FCE2BLauncher) RunDSHBuildWorker(ctx context.Context, objects storage.Storage) {
	if l == nil || l.Pool == nil || objects == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := l.withCurrentConfig()
			driver, err := newDSHBuildDriver(current, objects)
			if err != nil {
				continue
			}
			worker := dshprofile.BuildWorker{Ledger: dshprofile.PostgresBuildLedger{DB: current.Pool}, Driver: driver}
			if err := worker.Step(ctx); err != nil && !errors.Is(err, dshprofile.ErrNoBuildJob) && ctx.Err() == nil {
				slog.Warn("dsh_plugin_build_reconciliation_pending")
			}
		}
	}
}
