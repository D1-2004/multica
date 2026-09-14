package dshprofile

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBuildPostgresClaimsFencingAndDeletion(t *testing.T) {
	a, b := profilePools(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, fixture, driver := workerFixture()
	j := fixture.job
	if _, err := a.Exec(ctx, `INSERT INTO dsh_plugin_build(workspace_id,build_key,id,plugin_id,template_id,package_name,package_version,package_integrity,source_kind,source_spec,source_artifact_key)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, j.WorkspaceID, j.BuildKey, j.BuildID, j.Plugin.ID, j.TemplateID, j.Plugin.PackageName, j.Plugin.Version, j.Plugin.Integrity, j.Plugin.SourceKind, j.Plugin.SourceSpec, j.Plugin.ArtifactKey); err != nil {
		t.Fatal(err)
	}
	stores := []PostgresBuildLedger{{DB: a}, {DB: b}}
	type outcome struct {
		job BuildJob
		err error
	}
	results := make(chan outcome, 8)
	var group sync.WaitGroup
	for i := range 8 {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			job, err := stores[i%2].Claim(ctx)
			results <- outcome{job, err}
		}(i)
	}
	group.Wait()
	close(results)
	var claim BuildJob
	claimed := 0
	for result := range results {
		if result.err == nil {
			claimed++
			claim = result.job
		} else if !errors.Is(result.err, ErrNoBuildJob) {
			t.Fatal(result.err)
		}
	}
	if claimed != 1 || !claim.valid() {
		t.Fatal("replicas did not get one valid claim")
	}
	next := claim
	next.Phase, next.Intent, next.Scope, next.StartedAt = "creating", uuid.New(), driver.Scope(), time.Now().UTC().Truncate(time.Microsecond)
	next.ArtifactKey = next.objectKey()
	if err := stores[0].Save(ctx, claim, next); err != nil {
		t.Fatal(err)
	}
	if err := stores[1].Save(ctx, claim, next); !errors.Is(err, ErrBuildClaimLost) {
		t.Fatal("stale phase was not fenced", err)
	}
	// A delete transaction must not erase an intent whose create outcome is unknown.
	tx, err := b.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := GuardBuildDeletion(ctx, tx, j.WorkspaceID); !errors.Is(err, ErrBuildCleanupPending) {
		t.Fatal("active intent did not block ledger deletion", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// Expired claims are recoverable, but their old owner cannot bind a sandbox.
	if _, err := a.Exec(ctx, `UPDATE dsh_plugin_build SET claim_expires_at=now()-interval '1 second' WHERE workspace_id=$1 AND build_key=$2`, j.WorkspaceID, j.BuildKey); err != nil {
		t.Fatal(err)
	}
	recovered, err := stores[1].Claim(ctx)
	if err != nil || recovered.Intent != next.Intent || recovered.ClaimID == next.ClaimID || recovered.Phase != "creating" {
		t.Fatal("claim recovery changed create identity", err)
	}
	bound := next
	bound.Phase, bound.SandboxID = "starting", "sbx-fixture"
	if err := stores[0].Save(ctx, next, bound); !errors.Is(err, ErrBuildClaimLost) {
		t.Fatal("expired claimant bound a sandbox", err)
	}
	if err := stores[0].Release(ctx, next, 0); !errors.Is(err, ErrBuildClaimLost) {
		t.Fatal("expired claimant released replacement claim", err)
	}
	bound.ClaimID = recovered.ClaimID
	if err := stores[1].Save(ctx, recovered, bound); err != nil {
		t.Fatal(err)
	}
	running := bound
	running.Phase = "building"
	if err := stores[1].Save(ctx, bound, running); err != nil {
		t.Fatal(err)
	}
	publishing := running
	publishing.Phase, publishing.Artifact = "publishing", driver.observation.Artifact
	if err := stores[1].Save(ctx, running, publishing); err != nil {
		t.Fatal(err)
	}
	ready := publishing
	ready.Phase, ready.State = "cleanup", "ready"
	if err := stores[1].Save(ctx, publishing, ready); err != nil {
		t.Fatal(err)
	}
	done := ready
	done.Phase = "done"
	if err := stores[1].Save(ctx, ready, done); err != nil {
		t.Fatal(err)
	}
	if err := stores[1].Release(ctx, done, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].Claim(ctx); !errors.Is(err, ErrNoBuildJob) {
		t.Fatal("finished build was re-executed", err)
	}
	tx, err = b.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := GuardBuildDeletion(ctx, tx, j.WorkspaceID); err != nil {
		t.Fatal("cleaned build prevented deletion", err)
	}
}
