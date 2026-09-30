// Copied from github.com/DingTalk-Real-AI/dingtalk-workspace-cli tag v1.0.62-beta.8, path internal/logging/auth_debug.go; only import paths rewritten (and re-sorted by gofmt). Apache-2.0; see clicompat/NOTICE.
// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package logging

import (
	"log/slog"
	"os"
)

// AuthDebugEnv enables detailed authentication diagnostics when set to "1".
const AuthDebugEnv = "DWS_DEBUG_AUTH"

// AuthDebug writes detailed authentication diagnostics only when explicitly enabled.
func AuthDebug(message string, args ...any) {
	if os.Getenv(AuthDebugEnv) != "1" {
		return
	}
	slog.Debug(message, args...)
}
