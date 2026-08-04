package featureflag

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

const (
	EnvDiamondDataID = "MULTICA_DIAMOND_DATA_ID"
	EnvDiamondGroup  = "MULTICA_DIAMOND_GROUP"

	DefaultDiamondDataID = "dt-fde-multica.json"
	DefaultDiamondGroup  = "DEFAULT_GROUP"

	diamondEndpoint            = "jmenv.tbsite.net:8080"
	diamondEndpointContextPath = "diamond-server"
	diamondClusterName         = "diamond"
	diamondAppName             = "dt-fde-multica"
)

type diamondConfig struct {
	DataID string
	Group  string
}

type diamondClientSettings struct {
	Endpoint            string
	EndpointContextPath string
	ClusterName         string
	NamespaceID         string
	AppName             string
}

type diamondConfigClient interface {
	GetConfig(dataID, group string) (string, error)
	ListenConfig(dataID, group string, onChange func(string)) error
	CancelListenConfig(dataID, group string) error
	CloseClient()
}

type diamondClientFactory func(diamondClientSettings) (diamondConfigClient, error)

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

func defaultDiamondClientSettings() diamondClientSettings {
	return diamondClientSettings{
		Endpoint:            diamondEndpoint,
		EndpointContextPath: diamondEndpointContextPath,
		ClusterName:         diamondClusterName,
		NamespaceID:         "",
		AppName:             diamondAppName,
	}
}

func newNacosDiamondClient(settings diamondClientSettings) (diamondConfigClient, error) {
	stateDir := filepath.Join(os.TempDir(), "multica-diamond")
	clientConfig := constant.ClientConfig{
		TimeoutMs:           5000,
		NamespaceId:         settings.NamespaceID,
		AppName:             settings.AppName,
		Endpoint:            settings.Endpoint,
		EndpointContextPath: settings.EndpointContextPath,
		ClusterName:         settings.ClusterName,
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

func diamondConfigFromEnv() diamondConfig {
	dataID := strings.TrimSpace(os.Getenv(EnvDiamondDataID))
	if dataID == "" {
		dataID = DefaultDiamondDataID
	}
	group := strings.TrimSpace(os.Getenv(EnvDiamondGroup))
	if group == "" {
		group = DefaultDiamondGroup
	}
	return diamondConfig{DataID: dataID, Group: group}
}

func startDiamondListener(
	service *Service,
	provider *DiamondProvider,
	config diamondConfig,
	factory diamondClientFactory,
) {
	if factory == nil {
		logDiamondEvent(service.logger, "Diamond feature flags unavailable", config, 0, emptySHA256())
		return
	}
	client, err := factory(defaultDiamondClientSettings())
	if err != nil {
		logDiamondEvent(service.logger, "Diamond feature flags unavailable", config, 0, emptySHA256())
		return
	}

	content, err := client.GetConfig(config.DataID, config.Group)
	if err != nil {
		logDiamondEvent(service.logger, "Diamond feature flag initial fetch failed", config, 0, emptySHA256())
	} else {
		applyDiamondSnapshot(service.logger, provider, config, []byte(content))
	}

	err = client.ListenConfig(config.DataID, config.Group, func(content string) {
		applyDiamondSnapshot(service.logger, provider, config, []byte(content))
	})
	if err != nil {
		count, digest := provider.SnapshotMetadata()
		logDiamondEvent(service.logger, "Diamond feature flag listener unavailable", config, count, digest)
		client.CloseClient()
		return
	}

	service.setCloseFunc(func() error {
		err := client.CancelListenConfig(config.DataID, config.Group)
		client.CloseClient()
		count, digest := provider.SnapshotMetadata()
		logDiamondEvent(service.logger, "Diamond feature flag listener closed", config, count, digest)
		return err
	})
}

func applyDiamondSnapshot(logger *slog.Logger, provider *DiamondProvider, config diamondConfig, content []byte) {
	count, digest, err := provider.ApplyJSON(content)
	if err != nil {
		logDiamondEvent(logger, "Diamond feature flag update rejected", config, count, digest)
		return
	}
	logDiamondEvent(logger, "Diamond feature flags updated", config, count, digest)
}

func logDiamondEvent(logger *slog.Logger, message string, config diamondConfig, count int, digest string) {
	if logger == nil {
		return
	}
	logger.Info(message,
		slog.String("data_id", config.DataID),
		slog.String("group", config.Group),
		slog.Int("rules", count),
		slog.String("sha256", digest),
	)
}

func emptySHA256() string {
	sum := sha256.Sum256(nil)
	return hex.EncodeToString(sum[:])
}
