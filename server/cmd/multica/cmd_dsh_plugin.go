package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// `multica dsh-plugin` — the same operations the web surface offers, for
// scripting.
//
// The HTTP API is already scriptable on its own; what this adds is the CLI's
// existing login, workspace selection, file upload and error reporting, so
// importing a plugin in CI looks like every other Multica command rather than a
// hand-rolled curl with a token in it.

var dshPluginCmd = &cobra.Command{
	Use:     "dsh-plugin",
	Aliases: []string{"dsh-plugins"},
	Short:   "Manage DeepSeek Harness plugins for a workspace",
	Long: "Import, inspect and remove DSH plugins.\n\n" +
		"A plugin is an npm package whose manifest declares a bundle patch. It can be\n" +
		"imported by reference — npm:name@version, github:owner/repo#ref, or an https\n" +
		"tarball — or uploaded as a .zip or .tgz. Either way the server validates that\n" +
		"the package is something DeepSeek Harness can actually load before recording it.\n\n" +
		"Commands that take <plugin> accept either the package name `list` prints or\n" +
		"the plugin id.",
}

var dshPluginListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the plugins imported into this workspace",
	RunE:  runDshPluginList,
}

var dshPluginGetCmd = &cobra.Command{
	Use:   "get <plugin>",
	Short: "Show one plugin",
	Args:  cobra.ExactArgs(1),
	RunE:  runDshPluginGet,
}

var dshPluginImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import a plugin by reference or from a local archive",
	Example: "  multica dsh-plugin import --source npm:dsh-mcp-lens@0.1.0-rc.9\n" +
		"  multica dsh-plugin import --file ./my-plugin.zip\n" +
		"  multica dsh-plugin import --source github:owner/repo#v1.2.3 --on-conflict overwrite",
	RunE: runDshPluginImport,
}

var dshPluginFilesCmd = &cobra.Command{
	Use:   "files <plugin>",
	Short: "List the files inside a plugin's package",
	Args:  cobra.ExactArgs(1),
	RunE:  runDshPluginFiles,
}

var dshPluginCatCmd = &cobra.Command{
	Use:   "cat <plugin> <path>",
	Short: "Print one file from a plugin's package",
	Args:  cobra.ExactArgs(2),
	RunE:  runDshPluginCat,
}

var dshPluginCheckUpdateCmd = &cobra.Command{
	Use:   "check-update <plugin>",
	Short: "Report whether a newer version is published",
	Args:  cobra.ExactArgs(1),
	RunE:  runDshPluginCheckUpdate,
}

var dshPluginDeleteCmd = &cobra.Command{
	Use:   "delete <plugin>",
	Short: "Remove a plugin from the workspace",
	Args:  cobra.ExactArgs(1),
	RunE:  runDshPluginDelete,
}

// Importing a plugin does not make it run; binding it to an agent does. Without
// these three, a CLI-driven workflow could get a package into the workspace and
// then had to open the web UI to finish the job.

var dshPluginBindingsCmd = &cobra.Command{
	Use:   "bindings <agent>",
	Short: "List the plugins an agent boots with",
	Args:  cobra.ExactArgs(1),
	RunE:  runDshPluginBindings,
}

var dshPluginBindCmd = &cobra.Command{
	Use:     "bind <agent> <plugin>",
	Short:   "Bind a plugin to an agent",
	Example: "  multica dsh-plugin bind my-agent dsh-mcp-lens",
	Args:    cobra.ExactArgs(2),
	RunE:    runDshPluginBind,
}

var dshPluginUnbindCmd = &cobra.Command{
	Use:   "unbind <agent> <plugin>",
	Short: "Unbind a plugin from an agent",
	Args:  cobra.ExactArgs(2),
	RunE:  runDshPluginUnbind,
}

func init() {
	dshPluginImportCmd.Flags().String("source", "",
		"package reference: npm:name@version, github:owner/repo#ref, or an https tarball URL")
	dshPluginImportCmd.Flags().String("file", "", "local .zip or .tgz to upload")
	dshPluginImportCmd.Flags().String("display-name", "", "optional label for the plugin")
	// No `rename`: unlike a skill, a plugin's identity is its package name, so
	// the same package under a second name would be the same plugin twice.
	dshPluginImportCmd.Flags().String("on-conflict", "fail",
		"what to do if the package is already imported: fail, overwrite, skip")

	dshPluginDeleteCmd.Flags().Bool("yes", false, "skip the confirmation prompt")

	dshPluginBindCmd.Flags().Bool("disabled", false,
		"bind the plugin but leave it switched off")

	for _, command := range []*cobra.Command{
		dshPluginListCmd, dshPluginGetCmd, dshPluginImportCmd,
		dshPluginFilesCmd, dshPluginCatCmd, dshPluginCheckUpdateCmd,
		dshPluginBindingsCmd, dshPluginBindCmd, dshPluginUnbindCmd,
	} {
		command.Flags().String("output", "table", "Output format: table or json")
	}

	dshPluginCmd.AddCommand(dshPluginListCmd)
	dshPluginCmd.AddCommand(dshPluginGetCmd)
	dshPluginCmd.AddCommand(dshPluginImportCmd)
	dshPluginCmd.AddCommand(dshPluginFilesCmd)
	dshPluginCmd.AddCommand(dshPluginCatCmd)
	dshPluginCmd.AddCommand(dshPluginCheckUpdateCmd)
	dshPluginCmd.AddCommand(dshPluginDeleteCmd)
	dshPluginCmd.AddCommand(dshPluginBindingsCmd)
	dshPluginCmd.AddCommand(dshPluginBindCmd)
	dshPluginCmd.AddCommand(dshPluginUnbindCmd)
	rootCmd.AddCommand(dshPluginCmd)
}

func validDshPluginConflictStrategy(value string) bool {
	switch value {
	case "", "fail", "overwrite", "skip":
		return true
	}
	return false
}

type dshPluginSummary struct {
	ID              string   `json:"id"`
	PackageName     string   `json:"package_name"`
	DisplayName     string   `json:"display_name"`
	Description     string   `json:"description"`
	SourceKind      string   `json:"source_kind"`
	SourceSpec      string   `json:"source_spec"`
	ResolvedVersion string   `json:"resolved_version"`
	Integrity       string   `json:"integrity"`
	BundleRows      []string `json:"bundle_rows"`
	ConfigRow       string   `json:"config_row"`
	Catalog         string   `json:"catalog"`
}

// resolveDshPluginRef turns what a person typed into the id the API addresses.
//
// `list` prints the package name as the thing you read, so the package name is
// what gets typed next. The API takes a UUID, which is not in front of anyone.
// Without this the natural sequence -- list, then look at one -- fails on
// "invalid id", which reads as a broken command rather than a wrong argument.
//
// A UUID is passed straight through, so a script that already holds one costs
// nothing. Everything else is matched against the workspace's plugins: the
// package name first, since that is a plugin's identity and is unique per
// workspace, then the display name, which is a label and may not be.
func resolveDshPluginRef(ctx context.Context, client *cli.APIClient, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("a plugin id or package name is required")
	}
	if looksLikeDshPluginID(ref) {
		return canonicalUUID(ref), nil
	}

	var plugins []dshPluginSummary
	if err := client.GetJSON(ctx, "/api/dsh-plugins", &plugins); err != nil {
		return "", fmt.Errorf("look up %q: %w", ref, err)
	}
	return pickDshPlugin(plugins, ref)
}

// pickDshPlugin is the matching half of resolveDshPluginRef, kept separate from
// the request so the precedence and both error shapes are testable.
//
// An exact package-name hit wins outright: the package name is a plugin's
// identity and is unique in a workspace, so nothing else can be a better answer.
// Only when that misses does the display name come into play — it is a label,
// nobody promised it is unique, and an ambiguous one is worth saying so about
// rather than guessing.
func pickDshPlugin(plugins []dshPluginSummary, ref string) (string, error) {
	for _, plugin := range plugins {
		if plugin.PackageName == ref {
			return plugin.ID, nil
		}
	}
	var byLabel []dshPluginSummary
	for _, plugin := range plugins {
		if strings.EqualFold(plugin.DisplayName, ref) || strings.EqualFold(plugin.PackageName, ref) {
			byLabel = append(byLabel, plugin)
		}
	}
	switch len(byLabel) {
	case 1:
		return byLabel[0].ID, nil
	case 0:
		names := make([]string, 0, len(plugins))
		for _, plugin := range plugins {
			names = append(names, plugin.PackageName)
		}
		if len(names) == 0 {
			return "", fmt.Errorf("no DSH plugin %q is imported into this workspace, and neither is any other", ref)
		}
		return "", fmt.Errorf("no DSH plugin %q is imported into this workspace; imported: %s",
			ref, strings.Join(names, ", "))
	default:
		ids := make([]string, 0, len(byLabel))
		for _, plugin := range byLabel {
			ids = append(ids, plugin.PackageName+" ("+plugin.ID+")")
		}
		return "", fmt.Errorf("%q matches more than one plugin; use an id: %s",
			ref, strings.Join(ids, ", "))
	}
}

// looksLikeDshPluginID reports whether a ref is an id rather than a name.
// Deliberately a shape test, not a parse: the point is only to choose a branch,
// and an id that is well-shaped but wrong is the API's to reject.
//
// Wider than the shared uuidRegexp by one form. Before this resolver existed the
// argument went straight to the API, whose pgtype parser also accepts the
// undashed 32-hex spelling — so a script holding one would now fall through to
// the name lookup and be told its plugin does not exist, or, worse, match some
// unrelated package that happens to be named that.
func looksLikeDshPluginID(value string) bool {
	return uuidRegexp.MatchString(value) || undashedUUIDRe.MatchString(value)
}

var undashedUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// canonicalUUID spells an id the way the server writes it.
//
// Most routes take the id in the path, where pgtype parses it and any spelling
// works. `bind` is the exception: it sends ids in a JSON body that
// SetAgentDshPlugins looks up in a map keyed by uuidToString(row.ID) — a plain
// string compare — so an uppercase or undashed id that is perfectly valid comes
// back as "unknown DSH plugin". Normalising here keeps that from depending on
// how the caller happened to spell it.
func canonicalUUID(value string) string {
	lower := strings.ToLower(value)
	if len(lower) != 32 {
		return lower
	}
	return lower[0:8] + "-" + lower[8:12] + "-" + lower[12:16] + "-" +
		lower[16:20] + "-" + lower[20:32]
}

func runDshPluginList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var plugins []dshPluginSummary
	if err := client.GetJSON(ctx, "/api/dsh-plugins", &plugins); err != nil {
		return fmt.Errorf("list DSH plugins: %w", err)
	}
	if outputJSON(cmd) {
		return printJSON(plugins)
	}
	if len(plugins) == 0 {
		fmt.Println("No DSH plugins imported.")
		return nil
	}
	for _, plugin := range plugins {
		fmt.Printf("%s  %s@%s  [%s]\n", plugin.ID, plugin.PackageName,
			orDash(plugin.ResolvedVersion), plugin.SourceKind)
		if len(plugin.BundleRows) > 0 {
			fmt.Printf("    rows: %s\n", strings.Join(plugin.BundleRows, ", "))
		}
	}
	return nil
}

func runDshPluginGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	id, err := resolveDshPluginRef(ctx, client, args[0])
	if err != nil {
		return err
	}
	var plugin dshPluginSummary
	if err := client.GetJSON(ctx, "/api/dsh-plugins/"+id, &plugin); err != nil {
		return fmt.Errorf("get DSH plugin: %w", err)
	}
	if outputJSON(cmd) {
		return printJSON(plugin)
	}
	fmt.Printf("Package:    %s\n", plugin.PackageName)
	fmt.Printf("Version:    %s\n", orDash(plugin.ResolvedVersion))
	fmt.Printf("Source:     %s (%s)\n", orDash(plugin.SourceSpec), plugin.SourceKind)
	fmt.Printf("Integrity:  %s\n", orDash(plugin.Integrity))
	fmt.Printf("Rows:       %s\n", orDash(strings.Join(plugin.BundleRows, ", ")))
	if plugin.Description != "" {
		fmt.Printf("About:      %s\n", plugin.Description)
	}
	return nil
}

func runDshPluginImport(cmd *cobra.Command, _ []string) error {
	source, _ := cmd.Flags().GetString("source")
	file, _ := cmd.Flags().GetString("file")
	switch {
	case source == "" && file == "":
		return fmt.Errorf("either --source or --file is required")
	case source != "" && file != "":
		return fmt.Errorf("--source and --file are mutually exclusive")
	}
	onConflict, _ := cmd.Flags().GetString("on-conflict")
	if !validDshPluginConflictStrategy(onConflict) {
		return fmt.Errorf("--on-conflict must be one of: fail, overwrite, skip")
	}
	displayName, _ := cmd.Flags().GetString("display-name")

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	// Resolving a package means fetching and unpacking it, which is slower
	// than a normal API call.
	ctx, cancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(120*time.Second))
	defer cancel()

	var result map[string]any
	if file != "" {
		data, readErr := os.ReadFile(file)
		if readErr != nil {
			return fmt.Errorf("read plugin archive: %w", readErr)
		}
		if err := client.UploadDshPluginFile(
			ctx, data, filepath.Base(file), onConflict, displayName, &result,
		); err != nil {
			return fmt.Errorf("upload DSH plugin: %w", err)
		}
	} else {
		body := map[string]any{"source": source, "on_conflict": onConflict}
		if displayName != "" {
			body["display_name"] = displayName
		}
		if err := client.PostJSON(ctx, "/api/dsh-plugins", body, &result); err != nil {
			return fmt.Errorf("import DSH plugin: %w", err)
		}
	}
	return printDshPluginImportResult(cmd, result)
}

func printDshPluginImportResult(cmd *cobra.Command, result map[string]any) error {
	if outputJSON(cmd) {
		return printJSON(result)
	}
	status, _ := result["status"].(string)
	plugin, _ := result["plugin"].(map[string]any)
	if plugin == nil {
		plugin, _ = result["existing_plugin"].(map[string]any)
	}
	name, _ := plugin["package_name"].(string)
	version, _ := plugin["resolved_version"].(string)

	switch status {
	case "created":
		fmt.Printf("Imported %s@%s\n", name, version)
	case "updated":
		fmt.Printf("Updated %s@%s\n", name, version)
	case "skipped":
		fmt.Printf("Already imported: %s@%s\n", name, version)
	case "conflict":
		message, _ := result["error"].(string)
		return fmt.Errorf("%s", orDash(message))
	default:
		if message, _ := result["error"].(string); message != "" {
			return fmt.Errorf("%s", message)
		}
		fmt.Printf("Imported %s\n", orDash(name))
	}
	// Warnings are the whole point of validating at import: they say what will
	// go wrong later, while there is still someone reading.
	if warnings, ok := result["warnings"].([]any); ok {
		for _, warning := range warnings {
			if text, ok := warning.(string); ok && text != "" {
				fmt.Printf("  warning: %s\n", text)
			}
		}
	}
	if rows, ok := plugin["bundle_rows"].([]any); ok && len(rows) > 0 {
		names := make([]string, 0, len(rows))
		for _, row := range rows {
			if text, ok := row.(string); ok {
				names = append(names, text)
			}
		}
		fmt.Printf("  loader rows: %s\n", strings.Join(names, ", "))
	}
	return nil
}

func runDshPluginFiles(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var listing struct {
		PackageName string `json:"package_name"`
		Files       []struct {
			Path     string `json:"path"`
			Size     int64  `json:"size"`
			Viewable bool   `json:"viewable"`
		} `json:"files"`
		Truncated bool `json:"truncated"`
	}
	id, err := resolveDshPluginRef(ctx, client, args[0])
	if err != nil {
		return err
	}
	if err := client.GetJSON(ctx, "/api/dsh-plugins/"+id+"/files", &listing); err != nil {
		return fmt.Errorf("list plugin files: %w", err)
	}
	if outputJSON(cmd) {
		return printJSON(listing)
	}
	for _, file := range listing.Files {
		marker := " "
		if !file.Viewable {
			marker = "*"
		}
		fmt.Printf("%s %9d  %s\n", marker, file.Size, file.Path)
	}
	if listing.Truncated {
		fmt.Println("(listing truncated)")
	}
	fmt.Println("* not viewable as text")
	return nil
}

func runDshPluginCat(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var file struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	id, err := resolveDshPluginRef(ctx, client, args[0])
	if err != nil {
		return err
	}
	path := "/api/dsh-plugins/" + id + "/file?path=" + queryEscape(args[1])
	if err := client.GetJSON(ctx, path, &file); err != nil {
		return fmt.Errorf("read plugin file: %w", err)
	}
	if outputJSON(cmd) {
		return printJSON(file)
	}
	fmt.Print(file.Content)
	if !strings.HasSuffix(file.Content, "\n") {
		fmt.Println()
	}
	return nil
}

func runDshPluginCheckUpdate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var update struct {
		PackageName     string `json:"package_name"`
		CurrentVersion  string `json:"current_version"`
		LatestVersion   string `json:"latest_version"`
		UpdateAvailable bool   `json:"update_available"`
		Checkable       bool   `json:"checkable"`
		Reason          string `json:"reason"`
	}
	id, err := resolveDshPluginRef(ctx, client, args[0])
	if err != nil {
		return err
	}
	if err := client.GetJSON(ctx, "/api/dsh-plugins/"+id+"/update", &update); err != nil {
		return fmt.Errorf("check for an update: %w", err)
	}
	if outputJSON(cmd) {
		return printJSON(update)
	}
	if !update.Checkable {
		fmt.Printf("%s: %s\n", update.PackageName, orDash(update.Reason))
		return nil
	}
	if update.UpdateAvailable {
		fmt.Printf("%s: %s is published, this workspace has %s\n",
			update.PackageName, update.LatestVersion, update.CurrentVersion)
		return nil
	}
	fmt.Printf("%s: up to date (%s)\n", update.PackageName, update.CurrentVersion)
	return nil
}

func runDshPluginDelete(cmd *cobra.Command, args []string) error {
	var plugin dshPluginSummary
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	// Resolve before asking, not after. The prompt has to name what will
	// actually be removed: confirming a string the server has not looked up yet
	// is confirming nothing, and on a destructive command that is the whole
	// value of the prompt.
	lookupCtx, cancelLookup := cli.APIContext(context.Background())
	id, err := resolveDshPluginRef(lookupCtx, client, args[0])
	if err == nil {
		err = client.GetJSON(lookupCtx, "/api/dsh-plugins/"+id, &plugin)
		if err != nil {
			err = fmt.Errorf("get DSH plugin: %w", err)
		}
	}
	cancelLookup()
	if err != nil {
		return err
	}

	if skipConfirm, _ := cmd.Flags().GetBool("yes"); !skipConfirm {
		fmt.Printf("Remove DSH plugin %s@%s? Agents using it will stop loading it. [y/N]: ",
			plugin.PackageName, orDash(plugin.ResolvedVersion))
		var answer string
		_, _ = fmt.Scanln(&answer)
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// A second context, started after the prompt. The first one carries a
	// request deadline of about half a minute, and the prompt blocks on a human
	// typing — sharing it means someone who hesitates gets "context deadline
	// exceeded" from a delete they just confirmed.
	deleteCtx, cancelDelete := cli.APIContext(context.Background())
	defer cancelDelete()
	if err := client.DeleteJSON(deleteCtx, "/api/dsh-plugins/"+id); err != nil {
		return fmt.Errorf("delete DSH plugin: %w", err)
	}
	fmt.Printf("DSH plugin removed: %s\n", plugin.PackageName)
	return nil
}

// agentDshPluginBinding is one row of an agent's plugin set.
type agentDshPluginBinding struct {
	ID              string `json:"id"`
	PackageName     string `json:"package_name"`
	ResolvedVersion string `json:"resolved_version"`
	SourceKind      string `json:"source_kind"`
	Enabled         bool   `json:"enabled"`
}

func fetchAgentDshPlugins(ctx context.Context, client *cli.APIClient, agentID string) ([]agentDshPluginBinding, error) {
	var bound []agentDshPluginBinding
	if err := client.GetJSON(ctx, "/api/agents/"+agentID+"/dsh-plugins", &bound); err != nil {
		return nil, fmt.Errorf("list the agent's DSH plugins: %w", err)
	}
	return bound, nil
}

// resolveAgentExact resolves an agent by id or by its EXACT name.
//
// resolveAgent, which the autopilot commands use, matches a case-insensitive
// substring and errors only when two agents match. That is fine for reading;
// it is not fine here. With a single agent called "build-prod", `bind build`
// resolves silently to it, and these commands change what an agent runs. So an
// exact name is required, and a substring that would have matched is offered as
// a suggestion rather than acted on.
func resolveAgentExact(ctx context.Context, client *cli.APIClient, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("an agent id or name is required")
	}
	if uuidRegexp.MatchString(ref) {
		return ref, nil
	}
	if client.WorkspaceID == "" {
		return "", fmt.Errorf("workspace ID is required to resolve agents; use --workspace-id or set MULTICA_WORKSPACE_ID")
	}

	var agents []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		RuntimeMode string `json:"runtime_mode"`
	}
	path := "/api/agents?" + url.Values{"workspace_id": {client.WorkspaceID}}.Encode()
	if err := client.GetJSON(ctx, path, &agents); err != nil {
		return "", fmt.Errorf("fetch agents: %w", err)
	}

	var near []string
	for _, a := range agents {
		if a.Name == ref {
			return a.ID, nil
		}
		if strings.Contains(strings.ToLower(a.Name), strings.ToLower(ref)) {
			near = append(near, a.Name)
		}
	}
	if len(near) > 0 {
		return "", fmt.Errorf("no agent is named exactly %q; did you mean %s?",
			ref, strings.Join(near, ", "))
	}
	return "", fmt.Errorf("no agent named %q in this workspace", ref)
}

// warnIfAgentWillNotLoadPlugins prints a warning when the agent's runtime does
// not compose plugins at all.
//
// Plugins are assembled by the sandbox image's adapter; an agent on a local
// daemon stores the binding and never loads it. Reporting "Bound … (enabled)"
// with no further word would be the same silent no-op the web tab now warns
// about. Not an error: the binding is real and takes effect if the agent moves
// to a cloud runtime.
func warnIfAgentWillNotLoadPlugins(ctx context.Context, client *cli.APIClient, agentID string) {
	var agent struct {
		RuntimeMode string `json:"runtime_mode"`
	}
	if err := client.GetJSON(ctx, "/api/agents/"+agentID, &agent); err != nil {
		return
	}
	if agent.RuntimeMode != "" && agent.RuntimeMode != "cloud" {
		fmt.Fprintln(os.Stderr,
			"warning: this agent runs on a local daemon, which does not load DSH plugins. "+
				"The binding is saved but will not run until the agent moves to a cloud runtime.")
	}
}

func runDshPluginBindings(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	agentID, err := resolveAgentExact(ctx, client, args[0])
	if err != nil {
		return err
	}
	bound, err := fetchAgentDshPlugins(ctx, client, agentID)
	if err != nil {
		return err
	}
	if outputJSON(cmd) {
		return printJSON(bound)
	}
	if len(bound) == 0 {
		fmt.Println("No DSH plugins bound to this agent.")
		return nil
	}
	for _, binding := range bound {
		state := "enabled"
		if !binding.Enabled {
			state = "disabled"
		}
		fmt.Printf("%s  %s@%s  [%s] %s\n", binding.ID, binding.PackageName,
			orDash(binding.ResolvedVersion), binding.SourceKind, state)
	}
	return nil
}

// runDshPluginBind adds one plugin to the agent's set.
//
// Read-modify-write, because the endpoint REPLACES the set: sending only the
// plugin being added would silently unbind everything else the agent had. The
// web tab does the same thing for the same reason.
func runDshPluginBind(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	agentID, err := resolveAgentExact(ctx, client, args[0])
	if err != nil {
		return err
	}
	pluginID, err := resolveDshPluginRef(ctx, client, args[1])
	if err != nil {
		return err
	}
	bound, err := fetchAgentDshPlugins(ctx, client, agentID)
	if err != nil {
		return err
	}

	disabled, _ := cmd.Flags().GetBool("disabled")
	enabled := !disabled

	type entry struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	set := make([]entry, 0, len(bound)+1)
	replaced := false
	for _, binding := range bound {
		if binding.ID == pluginID {
			// Re-binding an already-bound plugin is how its enabled state is
			// flipped; keeping the old row as well would send the id twice.
			set = append(set, entry{ID: pluginID, Enabled: enabled})
			replaced = true
			continue
		}
		set = append(set, entry{ID: binding.ID, Enabled: binding.Enabled})
	}
	if !replaced {
		set = append(set, entry{ID: pluginID, Enabled: enabled})
	}

	if err := client.PutJSON(ctx, "/api/agents/"+agentID+"/dsh-plugins",
		map[string]any{"plugins": set}, nil); err != nil {
		return fmt.Errorf("bind DSH plugin: %w", err)
	}
	warnIfAgentWillNotLoadPlugins(ctx, client, agentID)
	if outputJSON(cmd) {
		return printJSON(map[string]any{
			"agent_id":  agentID,
			"plugin_id": pluginID,
			"enabled":   enabled,
			"bound":     len(set),
		})
	}
	state := "enabled"
	if disabled {
		state = "disabled"
	}
	fmt.Printf("Bound %s to the agent (%s). Plugins now bound: %d\n", args[1], state, len(set))
	return nil
}

func runDshPluginUnbind(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	agentID, err := resolveAgentExact(ctx, client, args[0])
	if err != nil {
		return err
	}
	pluginID, err := resolveDshPluginRef(ctx, client, args[1])
	if err != nil {
		return err
	}
	// A dedicated route, so this one needs no read-modify-write.
	if err := client.DeleteJSON(ctx, "/api/agents/"+agentID+"/dsh-plugins/"+pluginID); err != nil {
		return fmt.Errorf("unbind DSH plugin: %w", err)
	}
	if outputJSON(cmd) {
		return printJSON(map[string]any{
			"agent_id":  agentID,
			"plugin_id": pluginID,
			"unbound":   true,
		})
	}
	fmt.Printf("Unbound %s from the agent.\n", args[1])
	return nil
}

// outputJSON reports whether the caller asked for machine-readable output,
// using the same --output flag every other command takes.
func outputJSON(cmd *cobra.Command) bool {
	format, _ := cmd.Flags().GetString("output")
	return format == "json"
}

func printJSON(value any) error {
	return cli.PrintJSON(os.Stdout, value)
}

func queryEscape(value string) string {
	return url.QueryEscape(value)
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
