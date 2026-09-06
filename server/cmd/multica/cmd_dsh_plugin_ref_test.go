package main

import (
	"strings"
	"testing"
)

// `list` prints the package name, so the package name is what gets typed next.
// Before this resolver, every other subcommand answered "invalid id", which
// reads as a broken command rather than a wrong argument.
func TestLooksLikeDshPluginIDSeparatesAnIDFromAPackageName(t *testing.T) {
	t.Parallel()

	ids := []string{
		"f6039cff-0a61-4861-8d8f-87344d2eb793",
		"F6039CFF-0A61-4861-8D8F-87344D2EB793",
		// The undashed spelling the API's own UUID parser accepts. A script
		// holding one addressed these commands directly before the resolver
		// existed, so treating it as a name now would break it.
		"f6039cff0a6148618d8f87344d2eb793",
	}
	for _, value := range ids {
		if !looksLikeDshPluginID(value) {
			t.Errorf("looksLikeDshPluginID(%q) = false, want true", value)
		}
	}

	names := []string{
		"dsh-mcp-lens",
		"dsh-mcp-lens-uploaded",
		"@scope/dsh-plugin",
		// Close to the shape but not it: a package may be named anything, and
		// a near miss must go to the lookup rather than to the API.
		"f6039cff-0a61-4861-8d8f-87344d2eb79",
		"f6039cff-0a61-4861-8d8f-87344d2eb793-extra",
		"f6039cff0a6148618d8f87344d2eb79",
		"",
	}
	for _, value := range names {
		if looksLikeDshPluginID(value) {
			t.Errorf("looksLikeDshPluginID(%q) = true, want false", value)
		}
	}
}

func TestPickDshPluginPrefersAnExactPackageName(t *testing.T) {
	t.Parallel()

	plugins := []dshPluginSummary{
		{ID: "id-label", PackageName: "some-other-package", DisplayName: "dsh-mcp-lens"},
		{ID: "id-exact", PackageName: "dsh-mcp-lens", DisplayName: "MCP Lens"},
	}
	// The package name is a plugin's identity and is unique in a workspace, so
	// another plugin merely LABELLED with that name must not win — even when it
	// comes first in the list.
	got, err := pickDshPlugin(plugins, "dsh-mcp-lens")
	if err != nil {
		t.Fatalf("pickDshPlugin: %v", err)
	}
	if got != "id-exact" {
		t.Errorf("pickDshPlugin = %q, want the plugin whose package name matches", got)
	}
}

func TestPickDshPluginFallsBackToACaseInsensitiveMatch(t *testing.T) {
	t.Parallel()

	plugins := []dshPluginSummary{
		{ID: "id-1", PackageName: "dsh-mcp-lens", DisplayName: "MCP Lens"},
	}
	for _, ref := range []string{"DSH-MCP-LENS", "mcp lens"} {
		t.Run(ref, func(t *testing.T) {
			got, err := pickDshPlugin(plugins, ref)
			if err != nil {
				t.Fatalf("pickDshPlugin(%q): %v", ref, err)
			}
			if got != "id-1" {
				t.Errorf("pickDshPlugin(%q) = %q, want id-1", ref, got)
			}
		})
	}
}

// A display name is a label; nobody promised it is unique. Saying so beats
// picking one at random on a command that may be a delete.
func TestPickDshPluginRefusesAnAmbiguousLabel(t *testing.T) {
	t.Parallel()

	plugins := []dshPluginSummary{
		{ID: "id-1", PackageName: "plugin-one", DisplayName: "Lens"},
		{ID: "id-2", PackageName: "plugin-two", DisplayName: "Lens"},
	}
	_, err := pickDshPlugin(plugins, "Lens")
	if err == nil {
		t.Fatal("pickDshPlugin resolved an ambiguous label, want an error")
	}
	for _, want := range []string{"more than one", "plugin-one", "plugin-two"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestPickDshPluginSaysWhatIsImported(t *testing.T) {
	t.Parallel()

	plugins := []dshPluginSummary{{ID: "id-1", PackageName: "dsh-mcp-lens"}}
	_, err := pickDshPlugin(plugins, "nope")
	if err == nil {
		t.Fatal("pickDshPlugin found a plugin that is not there")
	}
	// The whole point of the resolver is that the caller does not know the ids,
	// so the miss has to show what they could have typed instead.
	if !strings.Contains(err.Error(), "dsh-mcp-lens") {
		t.Errorf("error %q does not list the imported plugins", err)
	}

	_, err = pickDshPlugin(nil, "nope")
	if err == nil {
		t.Fatal("pickDshPlugin found a plugin in an empty workspace")
	}
	if !strings.Contains(err.Error(), "neither is any other") {
		t.Errorf("error %q does not distinguish an empty workspace", err)
	}
}
