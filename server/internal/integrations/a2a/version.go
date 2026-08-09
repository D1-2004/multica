package a2aintegration

import (
	"context"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

const legacyDefaultProtocolVersion = "0.3"

// VersionInterceptor rejects requests that do not explicitly use A2A v1.0.
// Per the A2A service-parameter rules, an omitted version means legacy v0.3.
type VersionInterceptor struct {
	a2asrv.PassthroughCallInterceptor
}

// Before implements a2asrv.CallInterceptor.
func (VersionInterceptor) Before(
	ctx context.Context,
	callContext *a2asrv.CallContext,
	_ *a2asrv.Request,
) (context.Context, any, error) {
	version := legacyDefaultProtocolVersion
	if params := callContext.ServiceParams(); params != nil {
		if values, ok := params.Get(a2a.SvcParamVersion); ok && len(values) == 1 {
			if candidate := strings.TrimSpace(values[0]); candidate != "" {
				version = candidate
			}
		}
	}

	if version != string(a2a.Version) {
		return ctx, nil, a2a.NewError(
			a2a.ErrVersionNotSupported,
			fmt.Sprintf("A2A protocol version %q is not supported; use %s", version, a2a.Version),
		)
	}
	return ctx, nil, nil
}
