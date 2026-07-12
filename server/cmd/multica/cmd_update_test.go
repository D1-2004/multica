package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestRunUpdateRejectsNonPositiveDownloadTimeout(t *testing.T) {
	orig := updateDownloadTimeout
	updateDownloadTimeout = 0
	t.Cleanup(func() { updateDownloadTimeout = orig })

	err := runUpdate(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "download timeout must be greater than zero") {
		t.Fatalf("runUpdate error = %v, want download timeout validation", err)
	}
}

func TestUpdateCommandRegistersDownloadTimeoutFlag(t *testing.T) {
	flag := updateCmd.Flags().Lookup("download-timeout")
	if flag == nil {
		t.Fatal("updateCmd is missing --download-timeout")
	}
	if got := flag.DefValue; got != (120 * time.Second).String() {
		t.Fatalf("--download-timeout default = %q, want %q", got, (120 * time.Second).String())
	}
}

func TestUpdateCommandRegistersSourceFlags(t *testing.T) {
	for _, name := range []string{"source", "ref"} {
		if updateCmd.Flags().Lookup(name) == nil {
			t.Fatalf("updateCmd is missing --%s", name)
		}
	}
}

func TestRunUpdateUsesAndPersistsSource(t *testing.T) {
	origSource, origRef := updateSource, updateRef
	origBuild := updateBuildAndInstallSource
	origExecutable := updateCurrentExecutablePath
	origLoad := updateLoadSource
	origSave := updateSaveSource
	t.Cleanup(func() {
		updateSource, updateRef = origSource, origRef
		updateBuildAndInstallSource = origBuild
		updateCurrentExecutablePath = origExecutable
		updateLoadSource = origLoad
		updateSaveSource = origSave
	})

	updateSource = "fork"
	updateRef = ""
	updateCurrentExecutablePath = func() (string, error) { return "/tmp/multica", nil }
	updateLoadSource = func() (string, error) {
		t.Fatal("explicit --source must not load persisted source")
		return "", nil
	}
	var gotSpec cli.SourceSpec
	var gotRef, gotDestination, saved string
	updateBuildAndInstallSource = func(_ context.Context, spec cli.SourceSpec, ref, destination string) (string, error) {
		gotSpec, gotRef, gotDestination = spec, ref, destination
		return "built", nil
	}
	updateSaveSource = func(source string) error {
		saved = source
		return nil
	}

	if err := runUpdate(nil, nil); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	if gotSpec.Name != "fork" || gotRef != "develop" || gotDestination != "/tmp/multica" || saved != "fork" {
		t.Fatalf("source update = spec:%+v ref:%q destination:%q saved:%q", gotSpec, gotRef, gotDestination, saved)
	}
}

func TestRunUpdateBuildFailureDoesNotPersistSource(t *testing.T) {
	origSource, origRef := updateSource, updateRef
	origBuild := updateBuildAndInstallSource
	origExecutable := updateCurrentExecutablePath
	origSave := updateSaveSource
	t.Cleanup(func() {
		updateSource, updateRef = origSource, origRef
		updateBuildAndInstallSource = origBuild
		updateCurrentExecutablePath = origExecutable
		updateSaveSource = origSave
	})

	updateSource, updateRef = "official", "release-candidate"
	updateCurrentExecutablePath = func() (string, error) { return "/tmp/multica", nil }
	updateBuildAndInstallSource = func(_ context.Context, _ cli.SourceSpec, _, _ string) (string, error) {
		return "", errors.New("build failed")
	}
	updateSaveSource = func(string) error {
		t.Fatal("failed build must not persist source")
		return nil
	}
	if err := runUpdate(nil, nil); err == nil || !strings.Contains(err.Error(), "build failed") {
		t.Fatalf("runUpdate error = %v", err)
	}
}
