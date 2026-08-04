package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
)

func TestEnterpriseIdentityTokenRotationLockSerializesReplicas(t *testing.T) {
	pool := newSandboxLockPool(t)
	lockerA := newPostgresEnterpriseIdentityTokenRotationLocker(pool)
	lockerB := newPostgresEnterpriseIdentityTokenRotationLocker(pool)
	identityID := util.MustParseUUID("44444444-4444-4444-4444-444444444444")

	var active atomic.Int32
	var maxActive atomic.Int32
	start := make(chan struct{})
	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for _, locker := range []*postgresEnterpriseIdentityTokenRotationLocker{lockerA, lockerB} {
		wait.Add(1)
		go func(locker *postgresEnterpriseIdentityTokenRotationLocker) {
			defer wait.Done()
			<-start
			unlock, err := locker.Lock(context.Background(), identityID)
			if err != nil {
				errs <- err
				return
			}
			current := active.Add(1)
			for {
				seen := maxActive.Load()
				if current <= seen || maxActive.CompareAndSwap(seen, current) {
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
			active.Add(-1)
			unlock()
		}(locker)
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("enterprise identity token renewals overlapped across replicas: max active = %d", got)
	}
}
