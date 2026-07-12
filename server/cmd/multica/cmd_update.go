package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var updateDownloadTimeout time.Duration = cli.DefaultUpdateDownloadTimeout
var updateSource string
var updateRef string

var (
	updateBuildAndInstallSource = cli.BuildAndInstallSource
	updateCurrentExecutablePath = cli.CurrentExecutablePath
	updateLoadSource            = cli.LoadUpdateSource
	updateSaveSource            = cli.SaveUpdateSource
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update multica to the latest version",
	RunE:  runUpdate,
}

func init() {
	updateCmd.Flags().DurationVar(&updateDownloadTimeout, "download-timeout", cli.DefaultUpdateDownloadTimeout, "Maximum time to wait for the release archive download")
	updateCmd.Flags().StringVar(&updateSource, "source", "", "Update from source: fork or official (persisted after success)")
	updateCmd.Flags().StringVar(&updateRef, "ref", "", "Git branch, tag, or commit to build (defaults to the source's main branch)")
}

func runUpdate(_ *cobra.Command, _ []string) error {
	if updateDownloadTimeout <= 0 {
		return fmt.Errorf("download timeout must be greater than zero")
	}

	fmt.Fprintf(os.Stderr, "Current version: %s (commit: %s, built: %s)\n", version, commit, date)

	selectedSource := strings.TrimSpace(updateSource)
	if selectedSource == "" {
		var err error
		selectedSource, err = updateLoadSource()
		if err != nil {
			return err
		}
	}
	if selectedSource != "" {
		return runSourceUpdate(selectedSource, updateRef)
	}
	if strings.TrimSpace(updateRef) != "" {
		return fmt.Errorf("--ref requires --source fork|official or an existing persisted source")
	}

	// Check latest version from GitHub.
	latest, err := cli.FetchLatestRelease()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not check latest version: %v\n", err)
	} else {
		latestVer := strings.TrimPrefix(latest.TagName, "v")
		currentVer := strings.TrimPrefix(version, "v")
		if currentVer == latestVer {
			fmt.Fprintln(os.Stderr, "Already up to date.")
			return nil
		}
		fmt.Fprintf(os.Stderr, "Latest version:  %s\n\n", latest.TagName)
	}

	// Detect installation method and update accordingly.
	if cli.IsBrewInstall() {
		fmt.Fprintln(os.Stderr, "Updating via Homebrew...")
		output, err := cli.UpdateViaBrew()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", output)
			return fmt.Errorf("brew upgrade failed: %w\nYou can try manually: brew upgrade multica-ai/tap/multica", err)
		}
		fmt.Fprintln(os.Stderr, "Update complete.")
		return nil
	}

	// Not installed via brew — download binary directly from GitHub Releases.
	if latest == nil {
		return fmt.Errorf("could not determine latest version; check https://github.com/multica-ai/multica/releases/latest")
	}
	targetVersion := latest.TagName
	fmt.Fprintf(os.Stderr, "Downloading %s from GitHub Releases...\n", targetVersion)
	output, err := cli.UpdateViaDownloadWithTimeout(targetVersion, updateDownloadTimeout)
	if err != nil {
		return fmt.Errorf("update failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "%s\nUpdate complete.\n", output)
	return nil
}

func runSourceUpdate(sourceName, ref string) error {
	spec, err := cli.ResolveSource(sourceName)
	if err != nil {
		return err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = spec.DefaultRef
	}
	destination, err := updateCurrentExecutablePath()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Updating from %s source (%s)...\n", spec.Name, ref)
	output, err := updateBuildAndInstallSource(context.Background(), spec, ref, destination)
	if err != nil {
		return fmt.Errorf("source update failed: %w", err)
	}
	if err := updateSaveSource(spec.Name); err != nil {
		return fmt.Errorf("source update succeeded but could not persist source: %w", err)
	}
	fmt.Fprintf(os.Stderr, "%s\nUpdate complete.\n", output)
	return nil
}
