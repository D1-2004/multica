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

	if runtimeProvidersContent, providersErr := client.GetConfig(RuntimeProvidersDiamondDataID, DiamondGroup); providersErr != nil {
		if logger != nil {
			logger.Warn("Runtime provider catalog unavailable; application startup continues",
				slog.String("data_id", RuntimeProvidersDiamondDataID),
				slog.String("error", providersErr.Error()),
			)
		}
	} else if runtimeProviders, applyErr := service.ApplyRuntimeProvidersJSON([]byte(runtimeProvidersContent)); applyErr != nil {
		if logger != nil {
			logger.Warn("Runtime provider catalog rejected; application startup continues",
				slog.String("data_id", RuntimeProvidersDiamondDataID),
				slog.String("error", applyErr.Error()),
			)
		}
	} else {
		logRuntimeProvidersUpdate(logger, "Runtime provider catalog loaded", runtimeProviders)
	}
	modelPricingContent, err := client.GetConfig(ModelPricingDiamondDataID, DiamondGroup)
	if err != nil {
		return nil, fmt.Errorf("fetch required model pricing Diamond config: %w", err)
	}
	modelPricing, err := service.ApplyModelPricingJSON([]byte(modelPricingContent))
	if err != nil {
		return nil, fmt.Errorf("validate required model pricing Diamond config: %w", err)
	}
	logUpdate(logger, "runtime Diamond config loaded", snapshot)
	logModelPricingUpdate(logger, "model pricing Diamond config loaded", modelPricing)

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
	providersListening := false
	if err := client.ListenConfig(RuntimeProvidersDiamondDataID, DiamondGroup, func(content string) {
		next, applyErr := service.ApplyRuntimeProvidersJSON([]byte(content))
		if applyErr != nil {
			current := service.RuntimeProviders()
			if logger != nil {
				logger.Error("Runtime provider catalog update rejected; retaining previous snapshot",
					slog.String("data_id", RuntimeProvidersDiamondDataID),
					slog.Uint64("generation", current.Generation),
					slog.String("sha256", current.SHA256),
					slog.Int("count", len(current.Fingerprints)),
					slog.String("error", applyErr.Error()),
				)
			}
			return
		}
		logRuntimeProvidersUpdate(logger, "Runtime provider catalog updated", next)
	}); err != nil {
		if logger != nil {
			logger.Warn("Runtime provider catalog listener unavailable; application startup continues",
				slog.String("data_id", RuntimeProvidersDiamondDataID),
				slog.String("error", err.Error()),
			)
		}
	} else {
		providersListening = true
	}
	if err := client.ListenConfig(ModelPricingDiamondDataID, DiamondGroup, func(content string) {
		next, applyErr := service.ApplyModelPricingJSON([]byte(content))
		if applyErr != nil {
			current := service.ModelPricing()
			if logger != nil {
				logger.Error("model pricing Diamond update rejected; retaining previous snapshot",
					slog.String("data_id", ModelPricingDiamondDataID),
					slog.Uint64("generation", current.Generation),
					slog.String("sha256", current.SHA256),
					slog.Int("count", len(current.Models)),
					slog.String("error", applyErr.Error()),
				)
			}
			return
		}
		logModelPricingUpdate(logger, "model pricing Diamond config updated", next)
	}); err != nil {
		_ = client.CancelListenConfig(DiamondDataID, DiamondGroup)
		if providersListening {
			_ = client.CancelListenConfig(RuntimeProvidersDiamondDataID, DiamondGroup)
		}
		return nil, fmt.Errorf("listen to required model pricing Diamond config: %w", err)
	}
	service.setCloseFunc(func() error {
		runtimeErr := client.CancelListenConfig(DiamondDataID, DiamondGroup)
		var providersErr error
		if providersListening {
			providersErr = client.CancelListenConfig(RuntimeProvidersDiamondDataID, DiamondGroup)
		}
		pricingErr := client.CancelListenConfig(ModelPricingDiamondDataID, DiamondGroup)
		client.CloseClient()
		return errors.Join(runtimeErr, providersErr, pricingErr)
	})
	closeClient = false
	return service, nil
}

func logModelPricingUpdate(logger *slog.Logger, message string, snapshot ModelPricingSnapshot) {
	if logger == nil {
		return
	}
	logger.Info(message,
		slog.String("data_id", ModelPricingDiamondDataID),
		slog.Uint64("generation", snapshot.Generation),
		slog.String("sha256", snapshot.SHA256),
		slog.Int("count", len(snapshot.Models)),
	)
}

func logRuntimeProvidersUpdate(logger *slog.Logger, message string, snapshot RuntimeProvidersSnapshot) {
	if logger == nil {
		return
	}
	logger.Info(message,
		slog.String("data_id", RuntimeProvidersDiamondDataID),
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
