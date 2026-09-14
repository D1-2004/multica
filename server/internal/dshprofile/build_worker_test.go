package dshprofile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memoryBuildLedger struct {
	job       BuildJob
	claimed   bool
	failPhase string
	ackLost   bool
	saves     []string
}

func (s *memoryBuildLedger) Claim(context.Context) (BuildJob, error) {
	if s.claimed || s.job.Phase == "done" {
		return BuildJob{}, ErrNoBuildJob
	}
	s.claimed = true
	s.job.ClaimID = uuid.New()
	return s.job, nil
}
func (s *memoryBuildLedger) Save(_ context.Context, old, next BuildJob) error {
	if old.ClaimID != s.job.ClaimID || old.Phase != s.job.Phase {
		return ErrBuildClaimLost
	}
	if !validBuildTransition(old, next) {
		return errors.New("invalid transition in test")
	}
	if next.Phase == s.failPhase {
		s.failPhase = ""
		if s.ackLost {
			s.job = next
		}
		return errors.New("simulated SQL receipt failure")
	}
	s.job = next
	s.saves = append(s.saves, next.Phase)
	return nil
}
func (s *memoryBuildLedger) Release(_ context.Context, job BuildJob, _ time.Duration) error {
	if job.ClaimID == s.job.ClaimID {
		s.claimed = false
	}
	return nil
}

type buildDriverFixture struct {
	ledger                 *memoryBuildLedger
	creates, starts, finds int
	verifyCalls, destroys  int
	createError            bool
	startError             bool
	absenceUnconfirmed     bool
	verifyError            bool
	preflightError         bool
	observation            BuildObservation
	createdIntent          uuid.UUID
}

func (*buildDriverFixture) Scope() string { return "fc-test-scope" }
func (d *buildDriverFixture) Preflight(context.Context, BuildJob) error {
	if d.preflightError {
		return errors.New("source or provider missing")
	}
	return nil
}
func (d *buildDriverFixture) Create(_ context.Context, job BuildJob) (string, error) {
	d.creates++
	if d.ledger.job.Intent != job.Intent || d.ledger.job.Phase != "creating" {
		panic("create occurred before durable intent")
	}
	d.createdIntent = job.Intent
	if d.createError {
		return "", errors.New("transport lost after creation")
	}
	return "sbx-plugin-build", nil
}
func (d *buildDriverFixture) FindCreated(_ context.Context, job BuildJob) (string, error) {
	d.finds++
	if job.Intent != d.createdIntent {
		return "", ErrPending
	}
	return "sbx-plugin-build", nil
}
func (d *buildDriverFixture) Start(context.Context, BuildJob) error {
	d.starts++
	if d.startError {
		d.startError = false
		return errors.New("remote start response lost")
	}
	return nil
}
func (d *buildDriverFixture) Inspect(context.Context, BuildJob) (BuildObservation, error) {
	return d.observation, nil
}
func (d *buildDriverFixture) VerifyArtifact(context.Context, BuildJob) error {
	d.verifyCalls++
	if d.verifyError {
		return errors.New("object not readable")
	}
	return nil
}
func (d *buildDriverFixture) DestroyAndConfirmAbsent(context.Context, BuildJob) error {
	d.destroys++
	if d.absenceUnconfirmed {
		return errors.New("delete accepted but GET still running")
	}
	return nil
}

func workerFixture() (BuildWorker, *memoryBuildLedger, *buildDriverFixture) {
	p := SourcePlugin{ID: uuid.New(), PackageName: "fixture-plugin", Version: "1.0.0", Integrity: "sha256-" + strings.Repeat("a", 64),
		SourceKind: "upload", SourceSpec: "fixture-plugin@1.0.0", ArtifactKey: "fixture/plugin.tgz", Config: map[string]any{}}
	j := BuildJob{WorkspaceID: uuid.New(), BuildID: uuid.New(), TemplateID: "template-immutable", Plugin: p, State: "queued", Phase: "queued"}
	j.BuildKey = BuildKey(j.TemplateID, p)
	s := &memoryBuildLedger{job: j}
	d := &buildDriverFixture{ledger: s, observation: BuildObservation{State: "succeeded", Artifact: BuildArtifact{
		BuildDigest: strings.Repeat("a", 64), ArchiveSHA256: strings.Repeat("b", 64), ArchiveSize: 123, RuntimeLockSHA256: strings.Repeat("c", 64)}}}
	return BuildWorker{Ledger: s, Driver: d}, s, d
}

func advance(t *testing.T, w BuildWorker, count int) {
	t.Helper()
	for range count {
		if err := w.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildWorkerUnknownCreateAndSQLReceiptsNeverDuplicate(t *testing.T) {
	for _, failure := range []string{"transport", "before_intent", "after_intent", "before_bind", "after_bind"} {
		t.Run(failure, func(t *testing.T) {
			w, ledger, driver := workerFixture()
			switch failure {
			case "transport":
				driver.createError = true
			case "before_intent", "after_intent":
				ledger.failPhase, ledger.ackLost = "creating", failure == "after_intent"
			case "before_bind", "after_bind":
				ledger.failPhase, ledger.ackLost = "starting", failure == "after_bind"
			}
			if err := w.Step(context.Background()); err == nil {
				t.Fatal("injected loss was not observed")
			}
			if failure == "before_intent" || failure == "after_intent" {
				if driver.creates != 0 {
					t.Fatal("create after unconfirmed intent save")
				}
				if failure == "after_intent" {
					for range 3 {
						if err := w.Step(context.Background()); err == nil {
							t.Fatal("unknown intent incorrectly completed")
						}
					}
					if driver.creates != 0 || driver.finds != 3 {
						t.Fatal("unknown intent retried create")
					}
					return
				}
			}
			for ledger.job.Phase != "done" {
				advance(t, w, 1)
			}
			if driver.creates != 1 || driver.verifyCalls != 1 || driver.destroys != 1 || ledger.job.State != "ready" {
				t.Fatal("build did not converge with one create and verified publication")
			}
		})
	}
}

func TestBuildWorkerPublicationAndCleanupAreIndependentDurableGates(t *testing.T) {
	w, ledger, driver := workerFixture()
	advance(t, w, 3)
	if ledger.job.Phase != "publishing" || ledger.job.State != "queued" {
		t.Fatal("upload result alone became ready")
	}
	driver.verifyError = true
	if err := w.Step(context.Background()); err == nil || ledger.job.State != "queued" {
		t.Fatal("unreadable object accepted")
	}
	driver.verifyError = false
	advance(t, w, 1)
	if ledger.job.State != "ready" || ledger.job.Phase != "cleanup" {
		t.Fatal("verified artifact was not published")
	}
	driver.absenceUnconfirmed = true
	if err := w.Step(context.Background()); err == nil || ledger.job.Phase != "cleanup" {
		t.Fatal("DELETE without confirmed absence completed cleanup")
	}
	driver.absenceUnconfirmed = false
	advance(t, w, 1)
	if ledger.job.Phase != "done" || ledger.job.State != "ready" {
		t.Fatal("cleanup lost ready result")
	}
}

func TestBuildWorkerRejectsMalformedReceiptsAndIsolatesClaims(t *testing.T) {
	w, ledger, driver := workerFixture()
	driver.observation.Artifact.ArchiveSHA256 = "invalid"
	advance(t, w, 3)
	if ledger.job.State != "failed" || ledger.job.Phase != "cleanup" || driver.verifyCalls != 0 {
		t.Fatal("malformed receipt became publishable")
	}
	old := ledger.job
	ledger.job.ClaimID = uuid.New()
	next := old
	next.Phase = "done"
	if err := ledger.Save(context.Background(), old, next); !errors.Is(err, ErrBuildClaimLost) {
		t.Fatal("stale claimant changed lifecycle")
	}
	next = ledger.job
	next.WorkspaceID = uuid.New()
	if validBuildTransition(ledger.job, next) {
		t.Fatal("cross-workspace transition accepted")
	}
	next = ledger.job
	next.ArtifactKey = "another/workspace/object"
	if validBuildTransition(ledger.job, next) {
		t.Fatal("artifact key reassignment accepted")
	}
}

func TestBuildWorkerPreflightScopeAndTimeout(t *testing.T) {
	w, ledger, driver := workerFixture()
	driver.preflightError = true
	if err := w.Step(context.Background()); err == nil || driver.creates != 0 || ledger.job.Phase != "queued" {
		t.Fatal("create without prerequisites")
	}
	driver.preflightError = false
	advance(t, w, 1)
	ledger.job.Scope = "different-provider-account"
	if err := w.Step(context.Background()); err == nil || driver.starts != 0 {
		t.Fatal("provider scope drift accepted")
	}
	ledger.job.Scope = driver.Scope()
	w.Now = func() time.Time { return ledger.job.StartedAt.Add(31 * time.Minute) }
	advance(t, w, 2)
	if ledger.job.Phase != "done" || ledger.job.State != "failed" || ledger.job.ErrorCode != "build_timeout" {
		t.Fatal("timed out sandbox was not fenced and cleaned")
	}
}

type objectFixture struct{ value []byte }

func (s objectFixture) GetReader(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.value)), nil
}

func TestBuildObjectRequiresExactStoredBytes(t *testing.T) {
	w, ledger, _ := workerFixture()
	advance(t, w, 3)
	job := ledger.job
	value := []byte("artifact bytes")
	job.Artifact.ArchiveSHA256, job.Artifact.ArchiveSize = hash(value), int64(len(value))
	if err := VerifyBuildObject(context.Background(), objectFixture{value}, job); err != nil {
		t.Fatal(err)
	}
	for _, wrong := range [][]byte{value[:3], append(append([]byte{}, value...), 'x'), bytes.Repeat([]byte{'x'}, len(value))} {
		if err := VerifyBuildObject(context.Background(), objectFixture{wrong}, job); err == nil {
			t.Fatal("mismatched object became publishable")
		}
	}
}
