package service

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Provider scope is independent of the immutable artifact's capability list.
// An empty scope maintains the shared channel; a named FC scope owns a separate
// pointer and is excluded from subsequent shared-channel releases.
func ValidateStableProviderScope(backend SandboxBackendKind, scope string) error {
	if backend != SandboxBackendAliyunFC && backend != SandboxBackendASB {
		return errors.New("unsupported stable sandbox backend")
	}
	if scope == "" {
		return nil
	}
	if backend != SandboxBackendAliyunFC || !containsAllStrings(FCE2BSupportedProviders, scope) {
		return errors.New("provider_scope must name a supported FC provider")
	}
	return nil
}

func stableProviderChannel(scope string) string {
	if scope == "" {
		return "stable"
	}
	return "stable:" + scope
}

func stableReleaseProviders(manifest map[string]any) []string {
	if _, explicit := manifest["target_providers"]; explicit {
		return stringSliceMetadataValue(manifest, "target_providers")
	}
	return stringSliceMetadataValue(manifest, "providers")
}

func stableScopedProviders(providers []string, scope string, independent []string) ([]string, error) {
	if scope != "" {
		if !containsAllStrings(providers, scope) {
			return nil, errors.New("candidate artifact does not support the release provider")
		}
		return []string{scope}, nil
	}
	targets := make([]string, 0, len(providers))
	for _, provider := range providers {
		if !containsAllStrings(independent, provider) {
			targets = append(targets, provider)
		}
	}
	return targets, nil
}

func (s *FCE2BStableService) applyProviderScope(ctx context.Context, release FCE2BStableRelease, manifest map[string]any) error {
	rows, err := s.Pool.Query(ctx, `SELECT channel FROM fc_e2b_stable_channel
 WHERE sandbox_backend=$1 AND channel <> 'stable'`, release.SandboxBackend)
	if err != nil {
		return err
	}
	defer rows.Close()
	var independent []string
	for rows.Next() {
		var channel string
		if err := rows.Scan(&channel); err != nil {
			return err
		}
		independent = append(independent, strings.TrimPrefix(channel, "stable:"))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	targets, err := stableScopedProviders(stringSliceMetadataValue(manifest, "providers"), release.ProviderScope, independent)
	if err != nil {
		return err
	}
	manifest["target_providers"] = targets
	return nil
}

// Materialize a provider channel from the current shared pointer. This is part
// of release creation, under the backend channel lock, and rolls back if the
// release conflicts. It is never considered a completed/bootstrap rollout.
func initializeStableProviderChannel(ctx context.Context, tx pgx.Tx, backend SandboxBackendKind, scope string) error {
	if scope == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO fc_e2b_stable_channel
 (sandbox_backend,channel,artifact_kind,current_artifact_ref,current_artifact_build_id,
  current_artifact_digest,current_template_id,current_template_alias,current_release_id)
 SELECT sandbox_backend,$2,artifact_kind,current_artifact_ref,current_artifact_build_id,
  current_artifact_digest,current_template_id,current_template_alias,current_release_id
 FROM fc_e2b_stable_channel WHERE sandbox_backend=$1 AND channel='stable'
 ON CONFLICT (sandbox_backend,channel) DO NOTHING`, backend, stableProviderChannel(scope))
	return err
}
