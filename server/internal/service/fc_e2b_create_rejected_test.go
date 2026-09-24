package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/dshhost"
)

func TestRejectedSandboxCreateIsARetryableCreateFailure(t *testing.T) {
	err := fmt.Errorf("%w (HTTP %d)", dshhost.ErrCreateRejected, 403)
	failure := ClassifyRuntimeStartError(SandboxBackendAliyunFC, err)
	if !strings.HasSuffix(failure.Code, "-SANDBOX-CREATE-FAILED") || !failure.Retryable {
		t.Fatalf("rejected create must classify as a retryable sandbox create failure, got %+v", failure)
	}
}
