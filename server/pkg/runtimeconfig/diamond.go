package runtimeconfig

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

const (
	diamondEndpoint            = "jmenv.tbsite.net:8080"
	diamondEndpointContextPath = "diamond-server"
	diamondClusterName         = "diamond"
	diamondAppName             = "dt-fde-multica"
)

type diamondClient interface {
	GetConfig(dataID, group string) (string, error)
	ListenConfig(dataID, group string, onChange func(string)) error
	CancelListenConfig(dataID, group string) error
	CloseClient()
}

type diamondClientFactory func() (diamondClient, error)

type nacosDiamondClient struct {
	client config_client.IConfigClient
}

func (c *nacosDiamondClient) GetConfig(dataID, group string) (string, error) {
	return c.client.GetConfig(vo.ConfigParam{DataId: dataID, Group: group, Type: "json"})
}

func (c *nacosDiamondClient) ListenConfig(dataID, group string, onChange func(string)) error {
	return c.client.ListenConfig(vo.ConfigParam{
		DataId: dataID,
		Group:  group,
		Type:   "json",
		OnChange: func(_, _, _, data string) {
			onChange(data)
		},
	})
}

func (c *nacosDiamondClient) CancelListenConfig(dataID, group string) error {
	return c.client.CancelListenConfig(vo.ConfigParam{DataId: dataID, Group: group, Type: "json"})
}

func (c *nacosDiamondClient) CloseClient() {
	c.client.CloseClient()
}

func newNacosDiamondClient() (diamondClient, error) {
	stateDir := filepath.Join(os.TempDir(), "multica-runtime-diamond")
	clientConfig := constant.ClientConfig{
		TimeoutMs:           5000,
		NamespaceId:         "",
		AppName:             diamondAppName,
		Endpoint:            diamondEndpoint,
		EndpointContextPath: diamondEndpointContextPath,
		ClusterName:         diamondClusterName,
		CacheDir:            filepath.Join(stateDir, "cache"),
		LogDir:              filepath.Join(stateDir, "logs"),
		LogLevel:            "error",
		DisableUseSnapShot:  true,
		NotLoadCacheAtStart: true,
	}
	client, err := clients.NewConfigClient(vo.NacosClientParam{ClientConfig: &clientConfig})
	if err != nil {
		return nil, err
	}
	return &nacosDiamondClient{client: client}, nil
}

func newDiamondService(logger *slog.Logger, production bool, factory diamondClientFactory) (*Service, error) {
	if factory == nil {
		return nil, fmt.Errorf("runtime Diamond client factory is nil")
	}
	client, err := factory()
	if err != nil {
		return nil, fmt.Errorf("initialize runtime Diamond client: %w", err)
	}
	closeClient := true
	defer func() {
		if closeClient {
			client.CloseClient()
		}
	}()

	content, err := client.GetConfig(DiamondDataID, DiamondGroup)
	if err != nil {
		return nil, fmt.Errorf("fetch required runtime Diamond config: %w", err)
	}
	service := &Service{logger: logger, prod: production}
	snapshot, err := service.ApplyJSON([]byte(content))
	if err != nil {
		return nil, fmt.Errorf("validate required runtime Diamond config: %w", err)
	}

	manifestFingerprintsContent, err := client.GetConfig(ManifestFingerprintsDiamondDataID, DiamondGroup)
	if err != nil {
		return nil, fmt.Errorf("fetch required Runtime manifest fingerprints Diamond config: %w", err)
	}
	manifestFingerprints, err := service.ApplyManifestFingerprintsJSON([]byte(manifestFingerprintsContent))
	if err != nil {
		return nil, fmt.Errorf("validate required Runtime manifest fingerprints Diamond config: %w", err)
	}
	logUpdate(logger, "runtime Diamond config loaded", snapshot)
	logManifestFingerprintsUpdate(logger, "Runtime manifest fingerprints Diamond config loaded", manifestFingerprints)

	if err := client.ListenConfig(DiamondDataID, DiamondGroup, func(content string) {
		next, applyErr := service.ApplyJSON([]byte(content))
		if applyErr != nil {
			current := service.Current()
			if logger != nil {
				logger.Error("runtime Diamond update rejected; retaining previous snapshot",
					slog.Uint64("generation", current.Generation),
					slog.String("sha256", current.SHA256),
					slog.String("error", applyErr.Error()),
				)
			}
			return
		}
		logUpdate(logger, "runtime Diamond config updated", next)
	}); err != nil {
		return nil, fmt.Errorf("listen to required runtime Diamond config: %w", err)
	}
	if err := client.ListenConfig(ManifestFingerprintsDiamondDataID, DiamondGroup, func(content string) {
		next, applyErr := service.ApplyManifestFingerprintsJSON([]byte(content))
		if applyErr != nil {
			current := service.ManifestFingerprints()
			if logger != nil {
				logger.Error("Runtime manifest fingerprints Diamond update rejected; retaining previous snapshot",
					slog.String("data_id", ManifestFingerprintsDiamondDataID),
					slog.Uint64("generation", current.Generation),
					slog.String("sha256", current.SHA256),
					slog.Int("count", len(current.Fingerprints)),
					slog.String("error", applyErr.Error()),
				)
			}
			return
		}
		logManifestFingerprintsUpdate(logger, "Runtime manifest fingerprints Diamond config updated", next)
	}); err != nil {
		_ = client.CancelListenConfig(DiamondDataID, DiamondGroup)
		return nil, fmt.Errorf("listen to required Runtime manifest fingerprints Diamond config: %w", err)
	}
	service.setCloseFunc(func() error {
		runtimeErr := client.CancelListenConfig(DiamondDataID, DiamondGroup)
		fingerprintsErr := client.CancelListenConfig(ManifestFingerprintsDiamondDataID, DiamondGroup)
		client.CloseClient()
		return errors.Join(runtimeErr, fingerprintsErr)
	})
	closeClient = false
	return service, nil
}

func logManifestFingerprintsUpdate(logger *slog.Logger, message string, snapshot ManifestFingerprintsSnapshot) {
	if logger == nil {
		return
	}
	logger.Info(message,
		slog.String("data_id", ManifestFingerprintsDiamondDataID),
		slog.Uint64("generation", snapshot.Generation),
		slog.String("sha256", snapshot.SHA256),
		slog.Int("count", len(snapshot.Fingerprints)),
	)
}

func logUpdate(logger *slog.Logger, message string, snapshot Snapshot) {
	if logger == nil {
		return
	}
	logger.Info(message,
		slog.String("data_id", DiamondDataID),
		slog.String("group", DiamondGroup),
		slog.Int("schema_version", snapshot.Config.Version),
		slog.Uint64("generation", snapshot.Generation),
		slog.String("sha256", snapshot.SHA256),
		slog.Int("models", len(snapshot.Config.Runtime.LLM.Models)),
	)
}
