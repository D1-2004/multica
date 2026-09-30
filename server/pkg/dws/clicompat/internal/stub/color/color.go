// Package color stands in for github.com/fatih/color, which dws's
// internal/tui imports (vendored at clicompat/internal/upstream/tui). It is not dws
// code: it provides only the API subset tui uses and never emits ANSI codes,
// which is what fatih/color does when color.NoColor is set — its default
// whenever stdout is not a terminal, as when a host spawned dws. clicompat uses
// tui only through dws's errors package, whose human-readable renderer
// (PrintHuman) clicompat never calls. The stand-in avoids adding fatih/color
// and its dependencies to dws-for-tag and to Multica.
package color

import "fmt"

// Attribute is a text attribute (fatih/color's Attribute).
type Attribute int

// The attributes internal/tui uses (values as in fatih/color).
const (
	Bold      Attribute = 1
	Faint     Attribute = 2
	FgRed     Attribute = 31
	FgGreen   Attribute = 32
	FgYellow  Attribute = 33
	FgBlue    Attribute = 34
	FgCyan    Attribute = 36
	FgHiBlack Attribute = 90
	FgHiBlue  Attribute = 94
	FgHiWhite Attribute = 97
)

// Color is a set of attributes; this stand-in ignores them.
type Color struct{ attributes []Attribute }

// New returns a Color for the given attributes.
func New(value ...Attribute) *Color {
	return &Color{attributes: append([]Attribute(nil), value...)}
}

// SprintFunc returns fmt.Sprint: the uncolored output of fatih/color.
func (c *Color) SprintFunc() func(a ...interface{}) string {
	return func(a ...interface{}) string { return fmt.Sprint(a...) }
}
