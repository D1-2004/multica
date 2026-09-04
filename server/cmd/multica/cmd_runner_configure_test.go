package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRunnerConfigureCommandSavesFileAccessDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	first := filepath.Join(home, "projects")
	second := filepath.Join(home, "documents")
	for _, directory := range []string{first, second} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create test directory: %v", err)
		}
	}

	command, _, err := runnerCmd.Find([]string{"configure"})
	if err != nil {
		t.Fatalf("find runner configure command: %v", err)
	}
	if err := command.Flags().Set("directory", first+","+second); err != nil {
		t.Fatalf("set directory flag: %v", err)
	}
	t.Cleanup(func() { _ = command.Flags().Set("directory", "") })
	if err := command.RunE(command, nil); err != nil {
		t.Fatalf("run runner configure: %v", err)
	}

	config, err := loadRunnerConfig()
	if err != nil {
		t.Fatalf("load Runner config: %v", err)
	}
	if !reflect.DeepEqual(config.Roots, []string{first, second}) {
		t.Fatalf("file access directories = %#v", config.Roots)
	}
}
