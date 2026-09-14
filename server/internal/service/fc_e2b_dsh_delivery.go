package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshprofile"
)

func dshProfileInputPath(revision dshprofile.Revision) string {
	return dshhost.MountPath + "/home/.multica/profile-inputs/" + revision.Digest + ".json"
}

func (l *FCE2BLauncher) stageDSHProfile(ctx context.Context, host dshhost.Host, revision dshprofile.Revision) error {
	if revision.ID < 1 || len(revision.Descriptor) == 0 || len(revision.Descriptor) > 1024*1024 || revision.Digest != fmt.Sprintf("%x", sha256.Sum256([]byte(revision.Descriptor))) {
		return errors.New("invalid DSH Profile transfer")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(revision.Descriptor))
	const chunkSize = 60000
	count := (len(encoded) + chunkSize - 1) / chunkSize
	base := []string{"sandbox", "exec", "--user", "user", "-e", "LD_PRELOAD=", "-e", "LD_LIBRARY_PATH=", "-e", "PYTHONPATH=", "-e", "PYTHONHOME=",
		"-e", "MULTICA_DSH_WORKSPACE_ID=" + host.WorkspaceID.String(), "-e", "MULTICA_DSH_AGENT_ID=" + host.AgentID.String(),
		"-e", "MULTICA_DSH_PROFILE_DIGEST=" + revision.Digest,
		"-e", "MULTICA_DSH_PROFILE_PART_COUNT=" + strconv.Itoa(count)}
	command := []string{host.SandboxID, "--", "/opt/task-python/bin/python3", "-E", "-s", "/opt/multica-dsh/multica_dsh_profile.py", "--stage"}
	// Each invocation stays below both Linux's per-string limit and the
	// CLI host's argument budget; do not batch the entire 1 MiB descriptor.
	for index, offset := 0, 0; offset < len(encoded); index, offset = index+1, offset+chunkSize {
		args := append([]string{}, base...)
		args = append(args, "-e", "MULTICA_DSH_PROFILE_PART_INDEX="+strconv.Itoa(index),
			"-e", "MULTICA_DSH_PROFILE_PART="+encoded[offset:min(offset+chunkSize, len(encoded))])
		args = append(args, command...)
		out, err := l.runE2BCommandWithTimeout(ctx, time.Minute, args)
		if err != nil || len(out) > 65536 {
			return errors.New("DSH Profile part receipt unavailable")
		}
		var receipt struct {
			WorkspaceID string `json:"workspace_id"`
			AgentID     string `json:"agent_id"`
			Digest      string `json:"digest"`
			Index       *int   `json:"part_index"`
			Count       int    `json:"part_count"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(out)), &receipt) != nil || receipt.WorkspaceID != host.WorkspaceID.String() || receipt.AgentID != host.AgentID.String() || receipt.Digest != revision.Digest || receipt.Index == nil || *receipt.Index != index || receipt.Count != count {
			return errors.New("DSH Profile part receipt mismatch")
		}
	}
	args := append(append([]string{}, base...), command...)
	out, err := l.runE2BCommandWithTimeout(ctx, time.Minute, args)
	if err != nil || len(out) > 65536 {
		return errors.New("DSH Profile transfer receipt unavailable")
	}
	var receipt struct {
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		Revision    string `json:"revision"`
		Digest      string `json:"digest"`
		Path        string `json:"path"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &receipt); err != nil || receipt.WorkspaceID != host.WorkspaceID.String() || receipt.AgentID != host.AgentID.String() || receipt.Revision != strconv.FormatInt(revision.ID, 10) || receipt.Digest != revision.Digest || receipt.Path != dshProfileInputPath(revision) {
		return errors.New("DSH Profile transfer identity mismatch")
	}
	return nil
}

func (l *FCE2BLauncher) deliverDSHProfile(ctx context.Context, store dshprofile.Store, host dshhost.Host, revision dshprofile.Revision) error {
	var descriptor dshprofile.Descriptor
	if err := json.Unmarshal([]byte(revision.Descriptor), &descriptor); err != nil || descriptor.WorkspaceID != host.WorkspaceID || descriptor.AgentID != host.AgentID || revision.TemplateID != host.TemplateID {
		return errors.New("DSH artifact delivery identity mismatch")
	}
	for _, plugin := range descriptor.Plugins {
		var selected *dshprofile.Build
		for i := range revision.Builds {
			if revision.Builds[i].Digest == plugin.BuildDigest {
				if selected != nil {
					return errors.New("ambiguous DSH plugin artifact")
				}
				selected = &revision.Builds[i]
			}
		}
		if selected == nil || l.DSHArtifactSigner == nil {
			return errors.New("DSH plugin artifact unavailable")
		}
		delivery, err := store.Delivery(ctx, host.WorkspaceID, host.TemplateID, *selected)
		if err != nil {
			return errors.New("DSH plugin artifact publication unavailable")
		}
		url, err := l.DSHArtifactSigner.PresignGet(ctx, delivery.ArtifactKey, 15*time.Minute)
		if err != nil {
			return errors.New("DSH plugin delivery grant unavailable")
		}
		// Package bytes are independent of employee Loader settings. Do not
		// send the configuration or row ID to the installation endpoint.
		request := map[string]any{"operation": "ensure", "builds": dshhost.MountPath + "/plugin-builds", "url": url,
			"plugin":  map[string]string{"package_name": plugin.PackageName, "version": plugin.Version, "integrity": plugin.Integrity, "build_digest": plugin.BuildDigest},
			"receipt": delivery.Artifact}
		raw, err := json.Marshal(request)
		if err != nil || len(raw) > 65536 {
			return errors.New("invalid DSH plugin delivery request")
		}
		out, err := l.runE2BCommandWithTimeout(ctx, 5*time.Minute, []string{"sandbox", "exec", "--user", "user",
			"-e", "LD_PRELOAD=", "-e", "LD_LIBRARY_PATH=", "-e", "PYTHONPATH=", "-e", "PYTHONHOME=",
			"-e", "MULTICA_DSH_ARTIFACT_REQUEST=" + string(raw), host.SandboxID, "--",
			"/opt/task-python/bin/python3", "-E", "-s", "/opt/multica-dsh/multica_dsh_artifact.py"})
		if err != nil || len(out) > 65536 {
			return errors.New("DSH plugin installation receipt unavailable")
		}
		var receipt dshprofile.BuildArtifact
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &receipt); err != nil || receipt != delivery.Artifact {
			return errors.New("DSH plugin installation receipt mismatch")
		}
	}
	return nil
}
