// Copied from github.com/DingTalk-Real-AI/dingtalk-workspace-cli tag v1.0.62-beta.8, path internal/profilectx/profile.go; only import paths rewritten (and re-sorted by gofmt). Apache-2.0; see clicompat/NOTICE.
// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

// Package profilectx owns the process-local profile selector without importing
// authentication or transport packages.
package profilectx

import (
	"strings"
	"sync"
)

var (
	runtimeProfileMu sync.RWMutex
	runtimeProfile   string
)

// Set records the explicit profile selector for the current process.
func Set(profile string) {
	runtimeProfileMu.Lock()
	defer runtimeProfileMu.Unlock()
	runtimeProfile = strings.TrimSpace(profile)
}

// Get returns the explicit process-local profile selector.
func Get() string {
	runtimeProfileMu.RLock()
	defer runtimeProfileMu.RUnlock()
	return runtimeProfile
}
