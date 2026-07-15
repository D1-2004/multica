package agenttemplate

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"gopkg.in/yaml.v3"
)

const (
	BundleSchemaVersion int32 = 1
	DefaultSystemKey          = "fde-agent"
	DefaultSlug               = "fde-agent"
)

//go:embed seeds/fde-agent
var embeddedSeeds embed.FS

type Seed struct {
	SystemKey      string
	ReleaseVersion int64
	Slug           string
	DisplayName    string
	Description    string
	Bundle         agentsource.Bundle
	BundleJSON     []byte
}

type seedManifest struct {
	SystemKey      string `yaml:"system_key"`
	ReleaseVersion int64  `yaml:"release_version"`
	Slug           string `yaml:"slug"`
}

func LoadDefaultSeed(ctx context.Context) (Seed, error) {
	root, err := fs.Sub(embeddedSeeds, "seeds/fde-agent")
	if err != nil {
		return Seed{}, err
	}
	manifestBytes, err := fs.ReadFile(root, "seed.yaml")
	if err != nil {
		return Seed{}, fmt.Errorf("read seed manifest: %w", err)
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(manifestBytes)))
	decoder.KnownFields(true)
	var manifest seedManifest
	if err := decoder.Decode(&manifest); err != nil {
		return Seed{}, fmt.Errorf("decode seed manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Seed{}, errors.New("seed manifest must contain exactly one YAML document")
	}
	if strings.TrimSpace(manifest.SystemKey) == "" || strings.TrimSpace(manifest.Slug) == "" || manifest.ReleaseVersion < 1 {
		return Seed{}, errors.New("seed manifest requires system_key, slug, and a positive release_version")
	}
	bundle, err := agentsource.CompileFS(ctx, root)
	if err != nil {
		return Seed{}, fmt.Errorf("compile embedded seed: %w", err)
	}
	bundleJSON, err := json.Marshal(bundle)
	if err != nil {
		return Seed{}, fmt.Errorf("encode embedded seed: %w", err)
	}
	if len(bundleJSON) > agentsource.MaxBundleSize {
		return Seed{}, fmt.Errorf("encoded embedded seed exceeds %d bytes", agentsource.MaxBundleSize)
	}
	return Seed{
		SystemKey: manifest.SystemKey, ReleaseVersion: manifest.ReleaseVersion, Slug: manifest.Slug,
		DisplayName: bundle.Manifest.Metadata.Name, Description: bundle.Manifest.Metadata.Description,
		Bundle: bundle, BundleJSON: bundleJSON,
	}, nil
}

// ReconcileDefaultSeed stores only the highest released seed. Reusing a
// release version with different content is rejected so rolling deployments
// cannot silently move the seed backward or sideways.
func ReconcileDefaultSeed(ctx context.Context, queries *db.Queries) (Seed, error) {
	seed, err := LoadDefaultSeed(ctx)
	if err != nil {
		return Seed{}, err
	}
	_, err = queries.UpsertPlatformTemplateSeed(ctx, db.UpsertPlatformTemplateSeedParams{
		SystemKey: seed.SystemKey, ReleaseVersion: seed.ReleaseVersion,
		DisplayName: seed.DisplayName, Description: seed.Description,
		ContentHash: seed.Bundle.Hash, BundleSchemaVersion: BundleSchemaVersion,
		BundleSizeBytes: int32(len(seed.BundleJSON)), Bundle: seed.BundleJSON,
	})
	if err == nil {
		return seed, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Seed{}, fmt.Errorf("reconcile platform template seed: %w", err)
	}
	current, err := queries.GetPlatformTemplateSeed(ctx, seed.SystemKey)
	if err != nil {
		return Seed{}, fmt.Errorf("load current platform template seed: %w", err)
	}
	if current.ReleaseVersion == seed.ReleaseVersion && current.ContentHash != seed.Bundle.Hash {
		return Seed{}, fmt.Errorf("platform template seed release %d was reused with different content", seed.ReleaseVersion)
	}
	return seed, nil
}

func DecodeBundle(raw []byte, expectedHash string, expectedSize int32) (agentsource.Bundle, error) {
	// PostgreSQL JSONB normalizes key order and whitespace, so the byte length
	// returned by the driver is not the original encoded length. The persisted
	// size is a write-time guard and summary field; content integrity is proven
	// by the canonical Bundle hash below.
	if expectedSize <= 0 || expectedSize > agentsource.MaxBundleSize || len(raw) > agentsource.MaxBundleSize {
		return agentsource.Bundle{}, errors.New("agent template bundle size metadata is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var bundle agentsource.Bundle
	if err := decoder.Decode(&bundle); err != nil {
		return agentsource.Bundle{}, fmt.Errorf("decode agent template bundle: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return agentsource.Bundle{}, errors.New("agent template bundle must contain exactly one JSON value")
	}
	if bundle.Hash != expectedHash {
		return agentsource.Bundle{}, errors.New("agent template content hash does not match bundle")
	}
	if err := agentsource.ValidateBundle(bundle); err != nil {
		return agentsource.Bundle{}, err
	}
	return bundle, nil
}
