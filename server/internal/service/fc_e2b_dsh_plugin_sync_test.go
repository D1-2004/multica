package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOSCommandRunnerKeepsDiagnosticsOutsideReceipts(t *testing.T) {
	out, err := (OSCommandRunner{}).Run(context.Background(), "/bin/sh", []string{"-c", `printf '{"version":1}'; printf 'diagnostic\n' >&2`}, nil)
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("stderr polluted successful receipt: %q, %v", out, err)
	}
	out, err = (OSCommandRunner{}).Run(context.Background(), "/bin/sh", []string{"-c", `printf '{"event":"failed"}'; printf 'diagnostic\n' >&2; exit 1`}, nil)
	if err == nil || !strings.Contains(err.Error(), "diagnostic") || !json.Valid([]byte(out)) {
		t.Fatalf("failure diagnostics lost or stdout polluted: %q, %v", out, err)
	}
}

func TestDSHNativeSnapshotFailureEmitsOneReceipt(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "host")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '{\"event\":\"dsh_host_failed\"}\\n'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	command := strings.ReplaceAll(dshNativePluginSnapshotCommand, "/opt/multica-dsh/employee-profile-snapshot.mjs", helper)
	command = strings.ReplaceAll(command, "/usr/local/libexec/multica-dsh-host", helper)
	out, err := (OSCommandRunner{}).Run(context.Background(), "/bin/sh", []string{"-c", command}, nil)
	var receipt struct {
		Error string `json:"error"`
	}
	if err != nil || json.Unmarshal([]byte(out), &receipt) != nil || receipt.Error != "snapshot_unavailable" {
		t.Fatalf("helper diagnostic replaced error receipt: %q, %v", out, err)
	}
}
