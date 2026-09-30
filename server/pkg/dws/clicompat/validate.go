package clicompat

import (
	apperrors "github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/errors"
	"github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/transport"
)

// ValidateStrings is dws's pre-send input check, transport.Client.CallTool
// (internal/transport/client.go:665-668) running dws's validateCallArguments
// and validate.RejectControlChars (pkg/validate/input.go:31): control
// characters other than \t and \n, and U+200B–200D, U+FEFF, U+202A–202E,
// U+2028–2029, U+2066–2069 are rejected in top-level string arguments and in
// strings nested in map[string]any or directly inside []any (not in typed
// slices such as []string). The failure is a validation error, exit 3, and
// dws sends nothing. As in dws, which key is reported when several are bad
// follows Go's map order.
func ValidateStrings(args map[string]any) *Failure {
	if err := transport.ValidateCallArguments(args); err != nil {
		return newFailure(apperrors.NewValidation(err.Error()))
	}
	return nil
}
