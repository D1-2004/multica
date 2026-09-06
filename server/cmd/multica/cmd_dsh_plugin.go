package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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
		"the package is something DeepSeek Harness can actually load before recording it.",
}

var dshPluginListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the plugins imported into this workspace",
	RunE:  runDshPluginList,
}

var dshPluginGetCmd = &cobra.Command{
	Use:   "get <plugin-id>",
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
	Use:   "files <plugin-id>",
	Short: "List the files inside a plugin's package",
	Args:  cobra.ExactArgs(1),
	RunE:  runDshPluginFiles,
}

var dshPluginCatCmd = &cobra.Command{
	Use:   "cat <plugin-id> <path>",
	Short: "Print one file from a plugin's package",
	Args:  cobra.ExactArgs(2),
	RunE:  runDshPluginCat,
}

var dshPluginCheckUpdateCmd = &cobra.Command{
	Use:   "check-update <plugin-id>",
	Short: "Report whether a newer version is published",
	Args:  cobra.ExactArgs(1),
	RunE:  runDshPluginCheckUpdate,
}

var dshPluginDeleteCmd = &cobra.Command{
	Use:   "delete <plugin-id>",
	Short: "Remove a plugin from the workspace",
	Args:  cobra.ExactArgs(1),
	RunE:  runDshPluginDelete,
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

	for _, command := range []*cobra.Command{
		dshPluginListCmd, dshPluginGetCmd, dshPluginImportCmd,
		dshPluginFilesCmd, dshPluginCatCmd, dshPluginCheckUpdateCmd,
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

	var plugin dshPluginSummary
	if err := client.GetJSON(ctx, "/api/dsh-plugins/"+args[0], &plugin); err != nil {
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
	if err := client.GetJSON(ctx, "/api/dsh-plugins/"+args[0]+"/files", &listing); err != nil {
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
	path := "/api/dsh-plugins/" + args[0] + "/file?path=" + queryEscape(args[1])
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
	if err := client.GetJSON(ctx, "/api/dsh-plugins/"+args[0]+"/update", &update); err != nil {
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
	skipConfirm, _ := cmd.Flags().GetBool("yes")
	if !skipConfirm {
		fmt.Printf("Remove DSH plugin %s? Agents using it will stop loading it. [y/N]: ", args[0])
		var answer string
		_, _ = fmt.Scanln(&answer)
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println("Aborted.")
			return nil
		}
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	if err := client.DeleteJSON(ctx, "/api/dsh-plugins/"+args[0]); err != nil {
		return fmt.Errorf("delete DSH plugin: %w", err)
	}
	fmt.Printf("DSH plugin removed: %s\n", args[0])
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
