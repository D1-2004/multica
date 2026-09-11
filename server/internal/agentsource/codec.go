package agentsource

import (
	"encoding/json"
	"fmt"
)

// PackageCodec makes import and export inseparable. Registering a codec that
// omits either method, or uses different value types, is a compile-time error.
type PackageCodec[C, V any] interface {
	Import(*C, V) error
	Export(*C) (V, error)
}

// PackageField erases only the value type after the compiler checks the pair.
// Both directions retain the same JSON representation and field identifier.
type PackageField[C any] interface {
	Name() string
	Import(*C, json.RawMessage) error
	Export(*C) (json.RawMessage, error)
}

type packageField[C, V any] struct {
	name string
	codec PackageCodec[C, V]
}

func NewPackageField[C, V any](name string, codec PackageCodec[C, V]) PackageField[C] {
	return packageField[C, V]{name:name, codec:codec}
}

func (f packageField[C, V]) Name() string { return f.name }

func (f packageField[C, V]) Import(ctx *C, raw json.RawMessage) error {
	var value V
	if err := json.Unmarshal(raw, &value); err != nil { return fmt.Errorf("import %s: %w", f.name, err) }
	if err := f.codec.Import(ctx, value); err != nil { return fmt.Errorf("import %s: %w", f.name, err) }
	return nil
}

func (f packageField[C, V]) Export(ctx *C) (json.RawMessage, error) {
	value, err := f.codec.Export(ctx)
	if err != nil { return nil, fmt.Errorf("export %s: %w", f.name, err) }
	data, err := json.Marshal(value)
	if err != nil { return nil, fmt.Errorf("export %s: %w", f.name, err) }
	return data, nil
}
