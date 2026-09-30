// Not from dws: clicompat glue. Exposes unexported functions of this partly
// vendored package to clicompat, which reproduces the JSON branch of emitResult
// (internal/output/emitter.go:93). envelope.go and redaction.go are dws's,
// unmodified except for import paths; excerpts.go holds verbatim
// declarations from the package's other files.

package output

// RedactEnvelope is redactEnvelope (redaction.go).
func RedactEnvelope(source *Envelope) *Envelope {
	return redactEnvelope(source)
}
