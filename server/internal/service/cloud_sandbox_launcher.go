package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// CloudSandboxLauncher dispatches one task to the backend declared by the
// selected Runtime. It never changes backend after a launch failure.
type cloudSandboxRuntimeStore interface {
	GetAgentRuntime(context.Context, pgtype.UUID) (db.AgentRuntime, error)
}

type CloudSandboxLauncher struct {
	Queries  cloudSandboxRuntimeStore
	AliyunFC TaskRuntimeLauncher
	ASB      TaskRuntimeLauncher
}

func NewCloudSandboxLauncher(
	queries cloudSandboxRuntimeStore,
	aliyunFC TaskRuntimeLauncher,
	asb TaskRuntimeLauncher,
) *CloudSandboxLauncher {
	return &CloudSandboxLauncher{
		Queries:  queries,
		AliyunFC: aliyunFC,
		ASB:      asb,
	}
}

func (l *CloudSandboxLauncher) LaunchTask(ctx context.Context, task db.AgentTaskQueue) error {
	if l == nil || l.Queries == nil || !task.RuntimeID.Valid {
		return nil
	}
	runtime, err := l.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		return fmt.Errorf("load cloud sandbox runtime: %w", err)
	}
	metadata, err := ParseCloudSandboxRuntime(runtime)
	if errors.Is(err, ErrCloudSandboxRuntimeRequired) {
		return nil
	}
	if err != nil {
		return err
	}
	switch metadata.SandboxBackend {
	case SandboxBackendAliyunFC:
		if l.AliyunFC == nil {
			return errors.New("Aliyun FC sandbox launcher is unavailable")
		}
		return l.AliyunFC.LaunchTask(ctx, task)
	case SandboxBackendASB:
		if l.ASB == nil {
			return errors.New("ASB sandbox launcher is unavailable")
		}
		return l.ASB.LaunchTask(ctx, task)
	default:
		return ErrCloudSandboxMetadata
	}
}

type ASBEnterpriseRuntime struct {
	Launcher *ASBLauncher
	Identity *EnterpriseIdentityService
}

func NewASBEnterpriseRuntimeFromConfig(
	queries *db.Queries,
	tasks *TaskService,
	common *FCE2BLauncher,
	pool *pgxpool.Pool,
	asbConfig ASBConfig,
	identityConfig EnterpriseIdentityConfig,
) (*ASBEnterpriseRuntime, error) {
	if !asbConfig.Enabled && !identityConfig.Enabled {
		return nil, nil
	}
	if asbConfig.Enabled != identityConfig.Enabled {
		return nil, errors.New("ASB runtime and enterprise identity must be enabled together")
	}
	if queries == nil || tasks == nil || common == nil || pool == nil {
		return nil, errors.New("ASB enterprise runtime database dependencies are incomplete")
	}
	if err := asbConfig.Validate(); err != nil {
		return nil, err
	}
	if err := identityConfig.Validate(asbConfig); err != nil {
		return nil, err
	}
	asbClient, err := NewASBClient(ASBClientConfig{
		BaseURL: asbConfig.APIURL,
		APIKey:  asbConfig.APIKey,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	bucClient, err := NewHTTPBUCOAuthClient(
		identityConfig.BUCTokenURL,
		identityConfig.BUCIssuer,
		identityConfig.BUCJWKSURL,
		identityConfig.BUCClientID,
		identityConfig.BUCClientSecret,
		identityConfig.BUCRedirectURL,
		nil,
	)
	if err != nil {
		return nil, err
	}
	authXClient, err := NewNormandyAuthXClient(
		identityConfig.AuthXServiceID,
		identityConfig.AuthXAudience,
		identityConfig.AuthXTTL,
		identityConfig.AuthXEnvironment,
	)
	if err != nil {
		return nil, err
	}
	idemClient, err := NewIdemEnterpriseClient(identityConfig.IdemBaseURL, identityConfig.IdemTimeout)
	if err != nil {
		return nil, err
	}
	key, err := secretbox.LoadKey("MULTICA_ASB_IDENTITY_SECRET_KEY")
	if err != nil {
		return nil, err
	}
	secrets, err := secretbox.New(key)
	if err != nil {
		return nil, err
	}
	anchor := &ASBIdentityAnchorManager{Client: asbClient, Config: asbConfig}
	identity, err := NewEnterpriseIdentityService(
		queries,
		identityConfig,
		bucClient,
		authXClient,
		idemClient,
		anchor,
		secrets,
	)
	if err != nil {
		return nil, err
	}
	launcher := NewASBLauncher(queries, tasks, common, asbConfig, asbClient, identity)
	launcher.SetPool(pool)
	return &ASBEnterpriseRuntime{
		Launcher: launcher,
		Identity: identity,
	}, nil
}
