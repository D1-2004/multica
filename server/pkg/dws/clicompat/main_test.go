package clicompat

import (
	"os"
	"testing"

	"github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/i18n"
)

// TestMain pins dws's message language to English: the vendored i18n package
// picks it from DWS_LANG/LANG at start-up, as dws does, and the goldens were
// recorded with DWS_LANG=en.
func TestMain(m *testing.M) {
	i18n.SetLang("en")
	os.Exit(m.Run())
}
