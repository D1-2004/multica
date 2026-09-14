package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var runtimeStableCmd = &cobra.Command{
	Use:   "stable",
	Short: "Maintain cloud sandbox stable channels",
}

var runtimeStableChannelCmd = &cobra.Command{
	Use:   "channel",
	Short: "Show the current stable artifact and active release",
	RunE:  runRuntimeStableChannel,
}

var runtimeStableRuntimesCmd = &cobra.Command{
	Use:   "runtimes",
	Short: "List runtimes managed by a stable channel",
	RunE:  runRuntimeStableRuntimes,
}

var runtimeStableReleaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Create and maintain stable releases",
}

var runtimeStableReleaseListCmd = &cobra.Command{
	Use:   "list",
	Short: "List stable release history",
	RunE:  runRuntimeStableReleaseList,
}

var runtimeStableReleaseGetCmd = &cobra.Command{
	Use:   "get <release-id>",
	Short: "Show one stable release",
	Args:  exactArgs(1),
	RunE:  runRuntimeStableReleaseGet,
}

var runtimeStableReleaseCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create and validate a stable release",
	RunE:  runRuntimeStableReleaseCreate,
}

func init() {
	runtimeCmd.AddCommand(runtimeStableCmd)
	runtimeStableCmd.AddCommand(runtimeStableChannelCmd)
	runtimeStableCmd.AddCommand(runtimeStableRuntimesCmd)
	runtimeStableCmd.AddCommand(runtimeStableReleaseCmd)
	runtimeStableReleaseCmd.AddCommand(runtimeStableReleaseListCmd)
	runtimeStableReleaseCmd.AddCommand(runtimeStableReleaseGetCmd)
	runtimeStableReleaseCmd.AddCommand(runtimeStableReleaseCreateCmd)
	for _, action := range []string{
		"pause",
		"resume",
		"start-rollout",
		"advance-rollout",
		"complete-observation",
		"terminate",
		"rollback",
	} {
		runtimeStableReleaseCmd.AddCommand(newRuntimeStableReleaseActionCmd(action))
	}

	for _, command := range []*cobra.Command{
		runtimeStableChannelCmd,
		runtimeStableRuntimesCmd,
		runtimeStableReleaseListCmd,
	} {
		command.Flags().String("backend", "asb", "Sandbox backend: asb or aliyun_fc")
		command.Flags().String("provider", "", "Independent FC provider channel, for example dsh")
		command.Flags().String("output", "table", "Output format: table or json")
	}
	runtimeStableReleaseGetCmd.Flags().String("output", "json", "Output format: table or json")
	runtimeStableReleaseListCmd.Flags().String("status", "", "Only list releases with this status")
	runtimeStableReleaseListCmd.Flags().Int("limit", 20, "Maximum releases to return (1-100)")

	runtimeStableReleaseCreateCmd.Flags().String("backend", "asb", "Sandbox backend: asb or aliyun_fc")
	runtimeStableReleaseCreateCmd.Flags().String("provider", "", "Publish only this FC provider; other providers retain their channels")
	runtimeStableReleaseCreateCmd.Flags().String("artifact-ref", "", "Immutable ASB OCI image reference")
	runtimeStableReleaseCreateCmd.Flags().String("artifact-build-id", "", "Artifact build identifier")
	runtimeStableReleaseCreateCmd.Flags().String("artifact-built-at", "", "ASB artifact build time in RFC3339 format")
	runtimeStableReleaseCreateCmd.Flags().String("artifact-digest", "", "Artifact sha256 digest")
	runtimeStableReleaseCreateCmd.Flags().String("git-commit", "", "Source Git commit")
	runtimeStableReleaseCreateCmd.Flags().String("provider-fingerprint", "", "Runtime provider combination fingerprint")
	runtimeStableReleaseCreateCmd.Flags().String("template-id", "", "FC/E2B template ID")
	runtimeStableReleaseCreateCmd.Flags().String("note", "", "Operator note")
	runtimeStableReleaseCreateCmd.Flags().String("idempotency-key", "", "Required retry-safe operation key")
	runtimeStableReleaseCreateCmd.Flags().String("output", "json", "Output format: table or json")
}

func newRuntimeStableReleaseActionCmd(action string) *cobra.Command {
	command := &cobra.Command{
		Use:   action + " <release-id>",
		Short: stableReleaseActionDescription(action),
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRuntimeStableReleaseAction(cmd, args[0], action)
		},
	}
	command.Flags().String("output", "json", "Output format: table or json")
	return command
}

func stableReleaseActionDescription(action string) string {
	switch action {
	case "pause":
		return "Pause a stable release"
	case "resume":
		return "Resume a paused stable release"
	case "start-rollout":
		return "Start the timed stable rollout"
	case "advance-rollout":
		return "Advance to the next rollout batch"
	case "complete-observation":
		return "Complete the final observation window"
	case "terminate":
		return "Terminate a stable release"
	case "rollback":
		return "Roll back a stable release"
	default:
		return "Update a stable release"
	}
}

func runtimeStableBackend(cmd *cobra.Command) (string, error) {
	backend, _ := cmd.Flags().GetString("backend")
	backend = strings.ToLower(strings.TrimSpace(backend))
	if backend != "asb" && backend != "aliyun_fc" {
		return "", fmt.Errorf("--backend must be asb or aliyun_fc")
	}
	return backend, nil
}

func runRuntimeStableChannel(cmd *cobra.Command, _ []string) error {
	backend, err := runtimeStableBackend(cmd)
	if err != nil {
		return err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var response map[string]any
	path := "/api/runtimes/cloud-sandbox/stable-channel?sandbox_backend=" + url.QueryEscape(backend)
	if provider, _ := cmd.Flags().GetString("provider"); provider != "" {
		path += "&provider_scope=" + url.QueryEscape(provider)
	}
	if err := client.GetJSON(ctx, path, &response); err != nil {
		return fmt.Errorf("get stable channel: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, response)
	}

	current, _ := response["current"].(map[string]any)
	active, _ := response["active_release"].(map[string]any)
	rows := [][]string{{
		"current",
		strVal(current, "release_id"),
		"",
		strVal(current, "artifact_ref"),
	}}
	if active != nil {
		rows = append(rows, []string{
			"active",
			strVal(active, "id"),
			strVal(active, "status"),
			strVal(active, "artifact_ref"),
		})
	}
	cli.PrintTable(os.Stdout, []string{"KIND", "RELEASE_ID", "STATUS", "ARTIFACT"}, rows)
	return nil
}

func runRuntimeStableRuntimes(cmd *cobra.Command, _ []string) error {
	backend, err := runtimeStableBackend(cmd)
	if err != nil {
		return err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var runtimes []map[string]any
	path := "/api/runtimes/cloud-sandbox/stable-runtimes?sandbox_backend=" + url.QueryEscape(backend)
	if provider, _ := cmd.Flags().GetString("provider"); provider != "" {
		path += "&provider_scope=" + url.QueryEscape(provider)
	}
	if err := client.GetJSON(ctx, path, &runtimes); err != nil {
		return fmt.Errorf("list stable runtimes: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, runtimes)
	}
	rows := make([][]string, 0, len(runtimes))
	for _, runtime := range runtimes {
		rows = append(rows, []string{
			strVal(runtime, "runtime_id"),
			strVal(runtime, "runtime_name"),
			strVal(runtime, "provider"),
			strVal(runtime, "status"),
			strVal(runtime, "artifact_ref"),
			strVal(runtime, "matches_current_stable"),
			strVal(runtime, "active_release_target_status"),
		})
	}
	cli.PrintTable(os.Stdout, []string{"ID", "NAME", "PROVIDER", "STATUS", "ARTIFACT", "CURRENT", "TARGET_STATUS"}, rows)
	return nil
}

func runRuntimeStableReleaseList(cmd *cobra.Command, _ []string) error {
	backend, err := runtimeStableBackend(cmd)
	if err != nil {
		return err
	}
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 1 || limit > 100 {
		return fmt.Errorf("--limit must be between 1 and 100")
	}
	status, _ := cmd.Flags().GetString("status")
	query := url.Values{
		"sandbox_backend": {backend},
		"limit":           {strconv.Itoa(limit)},
	}
	if provider, _ := cmd.Flags().GetString("provider"); provider != "" {
		query.Set("provider_scope", provider)
	}
	if status = strings.TrimSpace(status); status != "" {
		query.Set("status", status)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var releases []map[string]any
	if err := client.GetJSON(ctx, "/api/runtimes/cloud-sandbox/stable-releases?"+query.Encode(), &releases); err != nil {
		return fmt.Errorf("list stable releases: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, releases)
	}
	rows := make([][]string, 0, len(releases))
	for _, release := range releases {
		rows = append(rows, stableReleaseRow(release))
	}
	cli.PrintTable(os.Stdout, stableReleaseHeaders(), rows)
	return nil
}

func runRuntimeStableReleaseGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var release map[string]any
	path := "/api/runtimes/cloud-sandbox/stable-releases/" + url.PathEscape(args[0])
	if err := client.GetJSON(ctx, path, &release); err != nil {
		return fmt.Errorf("get stable release: %w", err)
	}
	return printRuntimeStableRelease(cmd, release)
}

func runRuntimeStableReleaseCreate(cmd *cobra.Command, _ []string) error {
	backend, err := runtimeStableBackend(cmd)
	if err != nil {
		return err
	}
	idempotencyKey, _ := cmd.Flags().GetString("idempotency-key")
	if idempotencyKey = strings.TrimSpace(idempotencyKey); idempotencyKey == "" {
		return fmt.Errorf("--idempotency-key is required")
	}
	body := map[string]any{"sandbox_backend": backend}
	for flag, field := range map[string]string{
		"artifact-ref":         "artifact_ref",
		"artifact-build-id":    "artifact_build_id",
		"artifact-built-at":    "artifact_built_at",
		"artifact-digest":      "artifact_digest",
		"git-commit":           "git_commit",
		"provider-fingerprint": "provider_fingerprint",
		"template-id":          "template_id",
		"provider":             "provider_scope",
		"note":                 "note",
	} {
		value, _ := cmd.Flags().GetString(flag)
		if value = strings.TrimSpace(value); value != "" {
			body[field] = value
		}
	}
	if backend == "asb" {
		for _, field := range []string{"artifact_ref", "artifact_build_id", "artifact_built_at", "artifact_digest", "git_commit", "provider_fingerprint"} {
			if _, ok := body[field]; !ok {
				return fmt.Errorf("--%s is required for --backend asb", strings.ReplaceAll(field, "_", "-"))
			}
		}
	}
	if backend == "aliyun_fc" {
		for _, field := range []string{"template_id"} {
			if _, ok := body[field]; !ok {
				return fmt.Errorf("--%s is required for --backend aliyun_fc", strings.ReplaceAll(field, "_", "-"))
			}
		}
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var release map[string]any
	if err := client.PostJSONWithHeaders(
		ctx,
		"/api/runtimes/cloud-sandbox/stable-releases",
		body,
		&release,
		map[string]string{"Idempotency-Key": idempotencyKey},
	); err != nil {
		return fmt.Errorf("create stable release: %w", err)
	}
	return printRuntimeStableRelease(cmd, release)
}

func runRuntimeStableReleaseAction(cmd *cobra.Command, releaseID, action string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	path := "/api/runtimes/cloud-sandbox/stable-releases/" +
		url.PathEscape(releaseID) + "/" + action
	var release map[string]any
	if err := client.PostJSON(ctx, path, map[string]any{}, &release); err != nil {
		return fmt.Errorf("%s stable release: %w", action, err)
	}
	return printRuntimeStableRelease(cmd, release)
}

func printRuntimeStableRelease(cmd *cobra.Command, release map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, release)
	}
	cli.PrintTable(os.Stdout, stableReleaseHeaders(), [][]string{stableReleaseRow(release)})
	return nil
}

func stableReleaseHeaders() []string {
	return []string{"ID", "BACKEND", "PROVIDER_SCOPE", "STATUS", "ARTIFACT", "BUILT_AT", "BATCH", "UPDATED", "FAILED", "ERROR"}
}

func stableReleaseRow(release map[string]any) []string {
	return []string{
		strVal(release, "id"),
		strVal(release, "sandbox_backend"),
		strVal(release, "provider_scope"),
		strVal(release, "status"),
		strVal(release, "artifact_ref"),
		strVal(release, "artifact_built_at"),
		strVal(release, "current_batch"),
		strVal(release, "updated_targets"),
		strVal(release, "failed_targets"),
		strVal(release, "validation_error"),
	}
}
