package backend

import (
	"context"
	"fmt"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const configPath = "config"

func pathConfig(b *secretSyncBackend) *framework.Path {
	return &framework.Path{
		Pattern: configPath,
		Fields: map[string]*framework.FieldSchema{
			"disabled": {
				Type:        framework.TypeBool,
				Description: "Pause background remote mutations when true.",
			},
			"restore_guard": {
				Type:        framework.TypeBool,
				Description: "Require explicit operator acknowledgement before remote mutation after restore.",
			},
			"queue_capacity": {
				Type:        framework.TypeInt,
				Description: "Maximum number of pending outbox operations accepted by the mount.",
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation: &framework.PathOperation{
				Callback: b.pathConfigRead,
				Summary:  "Read global secret sync configuration.",
			},
			logical.UpdateOperation: &framework.PathOperation{
				Callback: b.pathConfigWrite,
				Summary:  "Update global secret sync configuration.",
			},
		},
		HelpSynopsis:    "Configure global secret sync behavior.",
		HelpDescription: "Controls mount-wide pause, restore guard, and queue capacity settings.",
	}
}

func pathConfigRestoreGuardAcknowledge(b *secretSyncBackend) *framework.Path {
	return &framework.Path{
		Pattern: "config/restore-guard/acknowledge",
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.UpdateOperation: &framework.PathOperation{
				Callback: b.pathConfigRestoreGuardAcknowledgeWrite,
				Summary:  "Acknowledge restore guard and resume remote mutation.",
			},
		},
		HelpSynopsis:    "Acknowledge restore guard.",
		HelpDescription: "Clears the restore guard after an operator has reviewed restore or clone safety.",
	}
}

func (b *secretSyncBackend) pathConfigRead(
	ctx context.Context,
	req *logical.Request,
	_ *framework.FieldData,
) (*logical.Response, error) {
	state, err := ensureRuntimeState(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	cfg, err := readGlobalConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	b.observer.RestoreGuardActive(ctx, cfg.RestoreGuard)
	return &logical.Response{Data: newResponseData(
		responseField("disabled", cfg.Disabled),
		responseField("restore_guard", cfg.RestoreGuard),
		responseField("restore_guard_acknowledged_time", cfg.RestoreGuardAcknowledgedTime),
		responseField("restore_epoch", state.RestoreEpoch.Epoch),
		responseField("plugin_instance_id", state.PluginInstance.ID),
		responseField("storage_schema_version", state.Schema.Version),
		responseField("storage_schema_min_compatible_version", state.Schema.MinCompatibleVersion),
		responseField("queue_capacity", cfg.QueueCapacity),
	)}, nil
}

func (b *secretSyncBackend) pathConfigWrite(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	cfg, err := readGlobalConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if value, ok := data.GetOk("disabled"); ok {
		cfg.Disabled = value.(bool)
	}
	if value, ok := data.GetOk("restore_guard"); ok {
		previousRestoreGuard := cfg.RestoreGuard
		cfg.RestoreGuard = value.(bool)
		if cfg.RestoreGuard {
			cfg.RestoreGuardAcknowledgedTime = ""
		} else {
			now := nowUTC().Format(timeFormatRFC3339)
			cfg.RestoreGuardAcknowledgedTime = now
			if previousRestoreGuard {
				if _, err := rotateRestoreEpoch(ctx, req.Storage, now); err != nil {
					return nil, err
				}
			}
		}
	}
	if value, ok := data.GetOk("queue_capacity"); ok {
		queueCapacity := value.(int)
		if queueCapacity < 0 {
			return logical.ErrorResponse("queue_capacity must be greater than or equal to zero"), nil
		}
		cfg.QueueCapacity = queueCapacity
	}
	if cfg.QueueCapacity == 0 {
		cfg.QueueCapacity = defaultQueueCapacity
	}
	cfg.UpdatedTime = nowUTC().Format(timeFormatRFC3339)
	if err := putGlobalConfig(ctx, req.Storage, cfg); err != nil {
		return nil, err
	}
	b.observer.RestoreGuardActive(ctx, cfg.RestoreGuard)
	return nil, nil
}

func (b *secretSyncBackend) pathConfigRestoreGuardAcknowledgeWrite(
	ctx context.Context,
	req *logical.Request,
	_ *framework.FieldData,
) (*logical.Response, error) {
	state, err := ensureRuntimeState(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	cfg, err := readGlobalConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	now := nowUTC().Format(timeFormatRFC3339)
	if cfg.RestoreGuard {
		state.RestoreEpoch, err = rotateRestoreEpoch(ctx, req.Storage, now)
		if err != nil {
			return nil, err
		}
	}
	cfg.RestoreGuard = false
	cfg.RestoreGuardAcknowledgedTime = now
	cfg.UpdatedTime = now
	if err := putGlobalConfig(ctx, req.Storage, cfg); err != nil {
		return nil, err
	}
	b.observer.RestoreGuardActive(ctx, cfg.RestoreGuard)
	return &logical.Response{Data: newResponseData(
		responseField("disabled", cfg.Disabled),
		responseField("restore_guard", cfg.RestoreGuard),
		responseField("restore_guard_acknowledged_time", cfg.RestoreGuardAcknowledgedTime),
		responseField("restore_epoch", state.RestoreEpoch.Epoch),
		responseField("plugin_instance_id", state.PluginInstance.ID),
		responseField("storage_schema_version", state.Schema.Version),
		responseField("storage_schema_min_compatible_version", state.Schema.MinCompatibleVersion),
		responseField("queue_capacity", cfg.QueueCapacity),
	)}, nil
}

func putGlobalConfig(ctx context.Context, storage logical.Storage, cfg globalConfig) error {
	entry, err := logical.StorageEntryJSON(configPath, cfg)
	if err != nil {
		return err
	}
	return storage.Put(ctx, entry)
}

func readGlobalConfig(ctx context.Context, storage logical.Storage) (globalConfig, error) {
	entry, err := storage.Get(ctx, configPath)
	if err != nil {
		return globalConfig{}, err
	}
	if entry == nil {
		return defaultGlobalConfig(), nil
	}
	var cfg globalConfig
	if err := entry.DecodeJSON(&cfg); err != nil {
		return globalConfig{}, err
	}
	if cfg.QueueCapacity < 0 {
		return globalConfig{}, fmt.Errorf("stored queue_capacity must be greater than or equal to zero")
	}
	if cfg.QueueCapacity == 0 {
		cfg.QueueCapacity = defaultQueueCapacity
	}
	return cfg, nil
}

func defaultGlobalConfig() globalConfig {
	return globalConfig{
		RestoreGuard:  true,
		QueueCapacity: defaultQueueCapacity,
	}
}

type globalConfig struct {
	Disabled                     bool   `json:"disabled"`
	RestoreGuard                 bool   `json:"restore_guard"`
	RestoreGuardAcknowledgedTime string `json:"restore_guard_acknowledged_time"`
	QueueCapacity                int    `json:"queue_capacity"`
	UpdatedTime                  string `json:"updated_time"`
}

const timeFormatRFC3339 = "2006-01-02T15:04:05Z07:00"
