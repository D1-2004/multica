//go:build linux

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// This lock is sandbox-local, never an NFS fencing primitive. PostgreSQL owns
// cross-sandbox writers. A crash witness blocks new preparation until the
// platform retires the sandbox, including any surviving preparation children.
func acquireNativeDSHWorkspace(ctx context.Context, native *agent.DSHNativeHostConfig) (func(bool), error) {
	root := "/tmp/multica-dsh-session-admission"
	if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("invalid native DSH admission directory")
	}
	file, err := os.OpenFile(filepath.Join(root, native.SessionID+".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			file.Close()
			return nil, err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	witness := filepath.Join(root, native.SessionID+".active")
	marker, err := os.OpenFile(witness, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		file.Close()
		return nil, errors.New("native DSH workspace has an unfinished owner; sandbox retirement is required")
	}
	_, writeErr := marker.WriteString(native.RequestID)
	syncErr := marker.Sync()
	marker.Close()
	if writeErr != nil || syncErr != nil {
		file.Close()
		return nil, errors.New("native DSH workspace admission could not be persisted")
	}
	release := func(quiescent bool) {
		if quiescent {
			_ = os.Remove(witness)
		}
		_ = file.Close()
	}
	if err := os.Mkdir(native.WorkDir, 0700); err != nil && !os.IsExist(err) {
		release(false)
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(native.WorkDir)
	if err != nil || resolved != native.WorkDir {
		release(false)
		return nil, errors.New("native DSH workspace resolves outside its binding")
	}
	return release, nil
}
