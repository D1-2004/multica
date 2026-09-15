package dshhost

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestFilesystemScopesShareStorageButCreateIndependentSandboxes(t *testing.T) {
	a, b := stores(t)
	ctx := context.Background()
	employee := bind(t, a)
	firstID, secondID := uuid.New(), uuid.New()
	first := FilesystemSandboxStore{DB: a.DB, ScopeID: firstID}
	second := FilesystemSandboxStore{DB: b.DB, ScopeID: secondID}
	for _, s := range []FilesystemSandboxStore{first, second} {
		if _, err := s.Bind(ctx, employee.Key); err != nil {
			t.Fatal(err)
		}
	}
	provider := &cloud{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 24 {
		s := first
		if i%2 == 1 {
			s = second
		}
		if i%3 == 0 {
			s.DB = b.DB
		}
		wg.Go(func() {
			<-start
			_, err := (Manager{Store: s, Provider: provider}).Ensure(ctx, employee.Key, "template")
			if err != nil && !errors.Is(err, ErrPending) && !errors.Is(err, ErrChanged) {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	one, err := first.Get(ctx, employee.Key)
	if err != nil {
		t.Fatal(err)
	}
	two, err := second.Get(ctx, employee.Key)
	if err != nil {
		t.Fatal(err)
	}
	if provider.creates != 2 || one.SandboxID == two.SandboxID || one.State != "running" || two.State != "running" || one.ScopeID != firstID || two.ScopeID != secondID {
		t.Fatalf("scopes did not create independent running instances: creates=%d, one=%+v, two=%+v", provider.creates, one, two)
	}
	if one.VolumeName != employee.VolumeName || two.VolumeName != employee.VolumeName || one.AccessPointARN != two.AccessPointARN {
		t.Fatal("execution scopes did not retain the same employee storage")
	}
	if _, err := second.BeginRetire(ctx, one); !errors.Is(err, ErrChanged) {
		t.Fatal("a scope retired another scope's sandbox", err)
	}
	provider.destroyErr = errors.New("unconfirmed destruction")
	if err := (Manager{Store: first, Provider: provider}).Retire(ctx, employee.Key, one.Generation); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if live, err := (Manager{Store: second, Provider: provider}).Ensure(ctx, employee.Key, "template"); err != nil || live.SandboxID != two.SandboxID {
		t.Fatal("unrelated session was blocked by retirement", err)
	}
}

func TestFilesystemScopeRejectsForeignTransitionBeforeDatabase(t *testing.T) {
	s := FilesystemSandboxStore{ScopeID: uuid.New()}
	h := Host{ScopeID: uuid.New()}
	if _, err := s.CompleteCreate(context.Background(), h, "sandbox"); !errors.Is(err, ErrChanged) {
		t.Fatal(err)
	}
	if _, err := s.BeginRetire(context.Background(), h); !errors.Is(err, ErrChanged) {
		t.Fatal(err)
	}
	if err := s.CompleteRetire(context.Background(), h); !errors.Is(err, ErrChanged) {
		t.Fatal(err)
	}
}
