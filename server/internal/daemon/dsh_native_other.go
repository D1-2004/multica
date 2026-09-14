//go:build !linux

package daemon

import (
	"context"
	"errors"
	"github.com/multica-ai/multica/server/pkg/agent"
)

func acquireNativeDSHWorkspace(context.Context, *agent.DSHNativeHostConfig) (func(bool), error) {
	return nil, errors.New("managed native DSH execution requires the FC Linux runtime")
}
