// Package startupobs carries optional startup instrumentation across providers.
// A missing recorder is a zero-work disabled path; payloads never include args,
// environment variables, credentials, command output or user content.
package startupobs

import (
	"context"
	"time"
)

type key struct{}
type Recorder func(context.Context, string, time.Time, error)

func WithRecorder(ctx context.Context, recorder Recorder) context.Context {
	return context.WithValue(ctx, key{}, recorder)
}
func Start(ctx context.Context, name string) func(error) {
	recorder, ok := ctx.Value(key{}).(Recorder)
	if !ok || recorder == nil {
		return func(error) {}
	}
	start := time.Now()
	return func(err error) { recorder(ctx, name, start, err) }
}
