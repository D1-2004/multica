package service

import (
	"context"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshprofile"
	"strconv"
	"time"
)

// Read the existing supervisor's control protocol. Unlike --ensure this cannot
// launch a process, stage a Profile or replace a live task's configuration.
const dshNativeHostHealthCommand = `import sys,json;sys.path.insert(0,"/opt/multica-dsh");from multica_dsh_host import control,employee_identity;from pathlib import Path;print(json.dumps(control(Path("/tmp/multica-dsh-host/control.sock"),employee_identity(),"health")))`

func (l *FCE2BLauncher) dshNativeHostHasProfile(ctx context.Context, host dshhost.Host, digest string, revision dshprofile.Revision) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := l.runE2BCommand(ctx, []string{"sandbox", "exec", "--user", "user", "-e", "LD_PRELOAD=", "-e", "LD_LIBRARY_PATH=", "-e", "PYTHONPATH=", "-e", "PYTHONHOME=",
		"-e", "DSH_HOME=" + dshhost.MountPath + "/home",
		"-e", "MULTICA_DSH_WORKSPACE_ID=" + host.WorkspaceID.String(),
		"-e", "MULTICA_DSH_AGENT_ID=" + host.AgentID.String(),
		"-e", "MULTICA_DSH_HOST_GENERATION=" + strconv.FormatInt(host.Generation, 10), host.SandboxID,
		"--", "/opt/task-python/bin/python3", "-c", dshNativeHostHealthCommand})
	return err == nil && validateDSHNativeHostReceipt(out, host, digest, revision) == nil
}
