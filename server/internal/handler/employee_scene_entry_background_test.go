package handler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmployeeSceneBackgroundReconcilesRunsButGatesNotices(t *testing.T) {
	f := employeeNoticeDatabase(t, "running", false, false)
	ctx, cancel := context.WithCancel(context.Background())
	worker := f.h.EmployeeSceneWorker
	var ready atomic.Bool
	checked := make(chan struct{}, 1)
	worker.ReplicaReady = func(context.Context) error {
		select {
		case checked <- struct{}{}:
		default:
		}
		if !ready.Load() {
			return errors.New("live replica lacks employee-loop:2")
		}
		return nil
	}
	// This models a bulk terminal update which bypassed the single-task bridge.
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='failed',error='Runtime start failed',completed_at=now() WHERE id=$1`, f.queueID); err != nil {
		cancel()
		t.Fatal(err)
	}
	go worker.Run(ctx)
	t.Cleanup(func() {
		cancel()
		if !worker.WaitWithTimeout(3 * time.Second) {
			t.Error("background worker did not stop")
		}
	})
	select {
	case <-checked:
	case <-time.After(3 * time.Second):
		t.Fatal("background reconciliation did not run")
	}
	var state string
	var notices int
	if err := testPool.QueryRow(ctx, `SELECT state FROM employee_task_run WHERE id=$1`, f.runID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("terminal bridge gated with notices: %s %v", state, err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_run_notice WHERE run_id=$1`, f.runID).Scan(&notices); err != nil || notices != 0 {
		t.Fatalf("old response worker could acquire a new notice: %d %v", notices, err)
	}
	ready.Store(true)
	for deadline := time.Now().Add(7 * time.Second); time.Now().Before(deadline); {
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_run_notice WHERE run_id=$1`, f.runID).Scan(&notices); err != nil {
			t.Fatal(err)
		}
		if notices == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("notice did not resume after every replica supported the protocol")
}
