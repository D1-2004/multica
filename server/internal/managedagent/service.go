package managedagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	DefaultSourceKey     = "fde-agent"
	DefaultRepositoryURL = "https://gitee.com/keeperqaq/fde-agent.git"
	DefaultRef           = "master"
	DefaultSyncInterval  = 30 * time.Minute
	DefaultBatchSize     = 50
	advisoryLockName     = "multica:managed-agent-source:fde-agent"
)

var ErrSnapshotUnavailable = errors.New("managed agent source snapshot is unavailable")

type Config struct {
	SourceKey     string
	RepositoryURL string
	Ref           string
	SyncInterval  time.Duration
	BatchSize     int32
}

func ConfigFromEnv() Config {
	return Config{
		SourceKey:     DefaultSourceKey,
		RepositoryURL: envOrDefault("MULTICA_FDE_AGENT_REPOSITORY_URL", DefaultRepositoryURL),
		Ref:           envOrDefault("MULTICA_FDE_AGENT_REPOSITORY_REF", DefaultRef),
		SyncInterval:  durationOrDefault(os.Getenv("MULTICA_FDE_AGENT_SYNC_INTERVAL"), DefaultSyncInterval),
		BatchSize:     int32OrDefault(os.Getenv("MULTICA_FDE_AGENT_ROLLOUT_BATCH_SIZE"), DefaultBatchSize),
	}
}

func (c Config) Enabled() bool { return strings.TrimSpace(c.RepositoryURL) != "" }

func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	parsed, err := url.Parse(c.RepositoryURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("MULTICA_FDE_AGENT_REPOSITORY_URL must be a credential-free public HTTPS Git repository URL")
	}
	pathParts := strings.Split(strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/"), "/")
	if len(pathParts) < 1 || slicesContain(pathParts, "") || slicesContain(pathParts, ".") || slicesContain(pathParts, "..") || strings.ContainsRune(c.Ref, '\x00') || strings.TrimSpace(c.Ref) == "" {
		return errors.New("invalid managed FDE Agent repository/ref configuration")
	}
	if c.BatchSize <= 0 || c.SyncInterval <= 0 {
		return errors.New("managed FDE Agent sync interval and rollout batch size must be positive")
	}
	return nil
}

func slicesContain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type Service struct {
	queries *db.Queries
	pool    *pgxpool.Pool
	config  Config
	logger  *slog.Logger
}

func New(queries *db.Queries, pool *pgxpool.Pool, config Config, logger *slog.Logger) (*Service, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{queries: queries, pool: pool, config: config, logger: logger}, nil
}

func (s *Service) Enabled() bool { return s != nil && s.config.Enabled() }

func (s *Service) Run(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	s.syncAndLog(ctx)
	ticker := time.NewTicker(s.config.SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncAndLog(ctx)
		}
	}
}

func (s *Service) syncAndLog(ctx context.Context) {
	changed, err := s.Sync(ctx)
	if err != nil {
		s.logger.Warn("managed FDE Agent source sync failed", "error", err)
		return
	}
	s.logger.Info("managed FDE Agent source checked", "changed", changed, "source_key", s.config.SourceKey)
}

// Sync serializes clone/compile/publish/rollout across API replicas with a
// session-scoped PostgreSQL advisory lock. A contender simply returns; the
// lock holder publishes the shared snapshot for every node.
func (s *Service) Sync(ctx context.Context) (bool, error) {
	if !s.Enabled() {
		return false, ErrSnapshotUnavailable
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1, 0))", advisoryLockName).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	defer func() {
		var unlocked bool
		if err := conn.QueryRow(context.Background(), "SELECT pg_advisory_unlock(hashtextextended($1, 0))", advisoryLockName).Scan(&unlocked); err != nil {
			s.logger.Warn("managed FDE Agent advisory unlock failed", "error", err)
		}
	}()

	sha, bundle, err := s.cloneAndCompile(ctx)
	if err != nil {
		message := truncate(err.Error(), 2000)
		_, recordErr := s.queries.UpsertManagedAgentSourceSnapshotFailure(ctx, db.UpsertManagedAgentSourceSnapshotFailureParams{
			SourceKey: s.config.SourceKey, RepositoryUrl: s.config.RepositoryURL, Ref: s.config.Ref,
			LastError: pgtype.Text{String: message, Valid: true},
		})
		if recordErr != nil {
			return false, fmt.Errorf("sync failed: %v; record failure: %w", err, recordErr)
		}
		return false, err
	}
	bundleJSON, err := json.Marshal(bundle)
	if err != nil {
		return false, err
	}
	previous, previousErr := s.queries.GetManagedAgentSourceSnapshot(ctx, s.config.SourceKey)
	changed := previousErr != nil || !previous.ResolvedCommitSha.Valid || previous.ResolvedCommitSha.String != sha || !previous.BundleHash.Valid || previous.BundleHash.String != bundle.Hash
	if _, err := s.queries.UpsertManagedAgentSourceSnapshotSuccess(ctx, db.UpsertManagedAgentSourceSnapshotSuccessParams{
		SourceKey: s.config.SourceKey, RepositoryUrl: s.config.RepositoryURL, Ref: s.config.Ref,
		ResolvedCommitSha: pgtype.Text{String: sha, Valid: true}, BundleHash: pgtype.Text{String: bundle.Hash, Valid: true}, Bundle: bundleJSON,
	}); err != nil {
		return false, err
	}
	if err := s.Rollout(ctx, sha, bundle); err != nil {
		return changed, err
	}
	return changed, nil
}

func (s *Service) Snapshot(ctx context.Context) (db.ManagedAgentSourceSnapshot, agentsource.Bundle, error) {
	row, err := s.queries.GetManagedAgentSourceSnapshot(ctx, s.config.SourceKey)
	if err != nil || !row.ResolvedCommitSha.Valid || len(row.Bundle) == 0 {
		return db.ManagedAgentSourceSnapshot{}, agentsource.Bundle{}, ErrSnapshotUnavailable
	}
	var bundle agentsource.Bundle
	if err := json.Unmarshal(row.Bundle, &bundle); err != nil || bundle.Hash == "" || bundle.Hash != row.BundleHash.String {
		return db.ManagedAgentSourceSnapshot{}, agentsource.Bundle{}, ErrSnapshotUnavailable
	}
	return row, bundle, nil
}

// EnsureSnapshot provides a first-use fallback. If another replica holds the
// sync lock, briefly wait for its shared PostgreSQL publication.
func (s *Service) EnsureSnapshot(ctx context.Context) (db.ManagedAgentSourceSnapshot, agentsource.Bundle, error) {
	if row, bundle, err := s.Snapshot(ctx); err == nil {
		return row, bundle, nil
	}
	_, _ = s.Sync(ctx)
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if row, bundle, err := s.Snapshot(ctx); err == nil {
			return row, bundle, nil
		}
		select {
		case <-ctx.Done():
			return db.ManagedAgentSourceSnapshot{}, agentsource.Bundle{}, ctx.Err()
		case <-deadline.C:
			return db.ManagedAgentSourceSnapshot{}, agentsource.Bundle{}, ErrSnapshotUnavailable
		case <-ticker.C:
		}
	}
}

func (s *Service) cloneAndCompile(ctx context.Context) (string, agentsource.Bundle, error) {
	dir, err := os.MkdirTemp("", "multica-managed-agent-*")
	if err != nil {
		return "", agentsource.Bundle{}, err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--single-branch", "--branch", s.config.Ref, "--", s.config.RepositoryURL, dir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", agentsource.Bundle{}, fmt.Errorf("clone managed repository: %w: %s", err, truncate(strings.TrimSpace(string(output)), 500))
	}
	shaOutput, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", agentsource.Bundle{}, fmt.Errorf("resolve managed repository commit: %w", err)
	}
	sha := strings.TrimSpace(string(shaOutput))
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		return "", agentsource.Bundle{}, fmt.Errorf("remove managed repository metadata: %w", err)
	}
	bundle, err := agentsource.CompileFS(ctx, os.DirFS(dir))
	if err != nil {
		return "", agentsource.Bundle{}, fmt.Errorf("compile managed repository: %w", err)
	}
	return sha, bundle, nil
}

// Provision materializes the latest shared snapshot into one workspace. The
// partial unique index makes retries and concurrent API replicas converge on
// the same Agent.
func (s *Service) Provision(ctx context.Context, workspaceID, ownerID, runtimeID pgtype.UUID, runtimeMode, model string) (db.Agent, bool, error) {
	if existing, err := s.queries.GetManagedAgentSourceInWorkspace(ctx, db.GetManagedAgentSourceInWorkspaceParams{WorkspaceID: workspaceID, ManagedSourceKey: pgtype.Text{String: s.config.SourceKey, Valid: true}}); err == nil {
		agent, err := s.updateExistingOwner(ctx, existing.AgentID, workspaceID, ownerID)
		return agent, false, err
	}
	snapshot, bundle, err := s.EnsureSnapshot(ctx)
	if err != nil {
		return db.Agent{}, false, err
	}
	if !providerAllowed(bundle.Manifest.Spec.Compatibility.Providers, "hermes") {
		return db.Agent{}, false, errors.New("managed FDE Agent manifest is not compatible with the fixed FC runtime")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return db.Agent{}, false, err
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)
	agent, err := qtx.CreateAgent(ctx, db.CreateAgentParams{
		WorkspaceID: workspaceID, Name: bundle.Manifest.Metadata.Name,
		Description: bundle.Manifest.Metadata.Description, Instructions: bundle.Instructions,
		RuntimeMode: runtimeMode, RuntimeConfig: []byte("{}"), RuntimeID: runtimeID,
		Visibility: "private", PermissionMode: "private", MaxConcurrentTasks: 6,
		OwnerID: ownerID, CustomEnv: []byte("{}"), CustomArgs: []byte("[]"),
		Model: pgtype.Text{String: model, Valid: strings.TrimSpace(model) != ""},
	})
	if err != nil {
		return db.Agent{}, false, err
	}
	owner, repo := repositoryParts(s.config.RepositoryURL)
	source, err := qtx.CreateManagedAgentSource(ctx, db.CreateManagedAgentSourceParams{
		AgentID: agent.ID, WorkspaceID: workspaceID,
		ManagedSourceKey: pgtype.Text{String: s.config.SourceKey, Valid: true},
		RepoOwner:        owner, RepoName: repo, Ref: s.config.Ref, ManifestPath: agentsource.ManifestPath,
		SyncedCommitSha: snapshot.ResolvedCommitSha.String, CreatedBy: ownerID,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		if existing, getErr := s.queries.GetManagedAgentSourceInWorkspace(ctx, db.GetManagedAgentSourceInWorkspaceParams{WorkspaceID: workspaceID, ManagedSourceKey: pgtype.Text{String: s.config.SourceKey, Valid: true}}); getErr == nil {
			agent, getAgentErr := s.updateExistingOwner(ctx, existing.AgentID, workspaceID, ownerID)
			return agent, false, getAgentErr
		}
		return db.Agent{}, false, err
	}
	for _, compiled := range bundle.Skills {
		skill, err := createManagedSkill(ctx, qtx, workspaceID, ownerID, source.ID, s.config, snapshot.ResolvedCommitSha.String, compiled)
		if err != nil {
			return db.Agent{}, false, err
		}
		if err := qtx.AddAgentSkill(ctx, db.AddAgentSkillParams{AgentID: agent.ID, SkillID: skill.ID}); err != nil {
			return db.Agent{}, false, err
		}
		if _, err := qtx.CreateAgentSourceSkill(ctx, db.CreateAgentSourceSkillParams{AgentSourceID: source.ID, SkillID: skill.ID, SourcePath: compiled.SourcePath}); err != nil {
			return db.Agent{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Agent{}, false, err
	}
	return agent, true, nil
}

func (s *Service) updateExistingOwner(ctx context.Context, agentID, workspaceID, ownerID pgtype.UUID) (db.Agent, error) {
	return s.queries.UpdateManagedAgentOwner(ctx, db.UpdateManagedAgentOwnerParams{
		AgentID: agentID, WorkspaceID: workspaceID, OwnerID: ownerID,
		ManagedSourceKey: pgtype.Text{String: s.config.SourceKey, Valid: true},
	})
}

func (s *Service) Rollout(ctx context.Context, sha string, bundle agentsource.Bundle) error {
	afterID := pgtype.UUID{Bytes: [16]byte{}, Valid: true}
	for {
		sources, err := s.queries.ListOutdatedIdleManagedAgentSources(ctx, db.ListOutdatedIdleManagedAgentSourcesParams{
			SourceKey: pgtype.Text{String: s.config.SourceKey, Valid: true}, TargetCommitSha: sha,
			AfterID: afterID, BatchSize: s.config.BatchSize,
		})
		if err != nil {
			return err
		}
		if len(sources) == 0 {
			return nil
		}
		for _, source := range sources {
			afterID = source.ID
			if err := s.rolloutOne(ctx, source, sha, bundle); err != nil {
				s.logger.Warn("managed FDE Agent rollout failed", "agent_id", uuidString(source.AgentID), "error", err)
			}
		}
		if len(sources) < int(s.config.BatchSize) {
			return nil
		}
	}
}

func (s *Service) ReconcileAgent(ctx context.Context, agentID pgtype.UUID) error {
	source, err := s.queries.GetAgentSourceByAgentID(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && source.SourceType != "managed_git") {
		return nil
	}
	if err != nil {
		return err
	}
	active, err := s.queries.AgentHasActiveTasks(ctx, agentID)
	if err != nil || active {
		return err
	}
	snapshot, bundle, err := s.Snapshot(ctx)
	if err != nil || source.SyncedCommitSha == snapshot.ResolvedCommitSha.String {
		return err
	}
	return s.rolloutOne(ctx, source, snapshot.ResolvedCommitSha.String, bundle)
}

func (s *Service) rolloutOne(ctx context.Context, source db.AgentSource, sha string, bundle agentsource.Bundle) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)
	locked, err := qtx.LockAgentSourceByAgentID(ctx, source.AgentID)
	if err != nil || locked.SourceType != "managed_git" || locked.SyncedCommitSha == sha {
		return err
	}
	active, err := qtx.AgentHasActiveTasks(ctx, source.AgentID)
	if err != nil || active {
		return err
	}
	agent, err := qtx.GetAgent(ctx, source.AgentID)
	if err != nil {
		return err
	}
	if _, err := qtx.UpdateAgent(ctx, db.UpdateAgentParams{ID: agent.ID, Instructions: pgtype.Text{String: bundle.Instructions, Valid: true}}); err != nil {
		return err
	}
	mappings, err := qtx.ListAgentSourceSkills(ctx, locked.ID)
	if err != nil {
		return err
	}
	byPath := make(map[string]db.AgentSourceSkill, len(mappings))
	for _, mapping := range mappings {
		byPath[mapping.SourcePath] = mapping
	}
	for _, compiled := range bundle.Skills {
		if mapping, ok := byPath[compiled.SourcePath]; ok {
			if err := updateManagedSkill(ctx, qtx, mapping.SkillID, locked.ID, s.config, sha, compiled); err != nil {
				return err
			}
			delete(byPath, compiled.SourcePath)
			continue
		}
		created, err := createManagedSkill(ctx, qtx, agent.WorkspaceID, agent.OwnerID, locked.ID, s.config, sha, compiled)
		if err != nil {
			return err
		}
		if err := qtx.AddAgentSkill(ctx, db.AddAgentSkillParams{AgentID: agent.ID, SkillID: created.ID}); err != nil {
			return err
		}
		if _, err := qtx.CreateAgentSourceSkill(ctx, db.CreateAgentSourceSkillParams{AgentSourceID: locked.ID, SkillID: created.ID, SourcePath: compiled.SourcePath}); err != nil {
			return err
		}
	}
	for _, removed := range byPath {
		if err := qtx.RemoveAgentSkill(ctx, db.RemoveAgentSkillParams{AgentID: agent.ID, SkillID: removed.SkillID}); err != nil {
			return err
		}
		if err := qtx.DeleteAgentSourceSkill(ctx, db.DeleteAgentSourceSkillParams{AgentSourceID: locked.ID, SkillID: removed.SkillID}); err != nil {
			return err
		}
		if err := qtx.DeleteSkill(ctx, db.DeleteSkillParams{ID: removed.SkillID, WorkspaceID: agent.WorkspaceID}); err != nil {
			return err
		}
	}
	if _, err := qtx.MarkAgentSourceSyncSucceeded(ctx, db.MarkAgentSourceSyncSucceededParams{ID: locked.ID, SyncedCommitSha: sha}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func createManagedSkill(ctx context.Context, q *db.Queries, workspaceID, creatorID, sourceID pgtype.UUID, config Config, sha string, compiled agentsource.Skill) (db.Skill, error) {
	encoded, _ := json.Marshal(skillConfig(config, sha, compiled.SourcePath))
	skill, err := q.CreateSkill(ctx, db.CreateSkillParams{
		WorkspaceID: workspaceID, Name: managedSkillName(compiled.Name, sourceID),
		Description: clean(compiled.Description), Content: clean(compiled.Content), Config: encoded, CreatedBy: creatorID,
	})
	if err != nil {
		return db.Skill{}, err
	}
	for _, file := range compiled.Files {
		if _, err := q.UpsertSkillFile(ctx, db.UpsertSkillFileParams{SkillID: skill.ID, Path: clean(file.Path), Content: clean(file.Content)}); err != nil {
			return db.Skill{}, err
		}
	}
	return skill, nil
}

func updateManagedSkill(ctx context.Context, q *db.Queries, skillID, sourceID pgtype.UUID, config Config, sha string, compiled agentsource.Skill) error {
	encoded, _ := json.Marshal(skillConfig(config, sha, compiled.SourcePath))
	if _, err := q.UpdateSkill(ctx, db.UpdateSkillParams{
		ID: skillID, Name: pgtype.Text{String: managedSkillName(compiled.Name, sourceID), Valid: true},
		Description: pgtype.Text{String: clean(compiled.Description), Valid: true},
		Content:     pgtype.Text{String: clean(compiled.Content), Valid: true}, Config: encoded,
	}); err != nil {
		return err
	}
	if err := q.DeleteSkillFilesBySkill(ctx, skillID); err != nil {
		return err
	}
	for _, file := range compiled.Files {
		if _, err := q.UpsertSkillFile(ctx, db.UpsertSkillFileParams{SkillID: skillID, Path: clean(file.Path), Content: clean(file.Content)}); err != nil {
			return err
		}
	}
	return nil
}

func skillConfig(config Config, sha, sourcePath string) map[string]any {
	return map[string]any{"origin": map[string]any{
		"type": "managed_git", "source_key": config.SourceKey, "repository": config.RepositoryURL,
		"ref": config.Ref, "commit_sha": sha, "path": sourcePath,
	}}
}

func managedSkillName(name string, sourceID pgtype.UUID) string {
	id := strings.ReplaceAll(uuidString(sourceID), "-", "")
	if len(id) > 16 {
		id = id[:16]
	}
	if id == "" {
		return clean(strings.TrimSpace(name))
	}
	return clean(strings.TrimSpace(name)) + "--" + id
}

func repositoryParts(raw string) (string, string) {
	parsed, _ := url.Parse(raw)
	parts := strings.Split(strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/"), "/")
	if len(parts) < 2 {
		return "managed", "fde-agent"
	}
	return parts[len(parts)-2], parts[len(parts)-1]
}

func providerAllowed(allowed []string, provider string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if candidate == provider {
			return true
		}
	}
	return false
}

func clean(value string) string { return strings.ReplaceAll(value, "\x00", "") }
func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}
func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
func durationOrDefault(raw string, fallback time.Duration) time.Duration {
	if value, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil && value > 0 {
		return value
	}
	return fallback
}
func int32OrDefault(raw string, fallback int) int32 {
	var value int
	if _, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &value); err == nil && value > 0 {
		return int32(value)
	}
	return int32(fallback)
}
