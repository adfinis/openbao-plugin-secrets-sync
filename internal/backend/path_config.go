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
			"require_source_opt_in": {
				Type: framework.TypeBool,
				Description: "Require custom_metadata.syncable=true before enabled associations " +
					"can enqueue or dispatch remote mutation.",
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
		HelpDescription: "Controls mount-wide pause, restore guard, queue capacity, and source opt-in settings.",
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
		responseField("require_source_opt_in", cfg.RequireSourceOptIn),
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
	if value, ok := data.GetOk("require_source_opt_in"); ok {
		cfg.RequireSourceOptIn = value.(bool)
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
		responseField("require_source_opt_in", cfg.RequireSourceOptIn),
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
	cfg, _, err := decodeGlobalConfigEntry(entry)
	if err != nil {
		return globalConfig{}, err
	}
	return cfg, nil
}

func ensureGlobalConfig(ctx context.Context, storage logical.Storage, now string) error {
	entry, err := storage.Get(ctx, configPath)
	if err != nil {
		return err
	}
	if entry == nil {
		return putGlobalConfig(ctx, storage, initialGlobalConfig(now))
	}
	cfg, changed, err := decodeGlobalConfigEntry(entry)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	cfg.UpdatedTime = now
	return putGlobalConfig(ctx, storage, cfg)
}

func decodeGlobalConfigEntry(entry *logical.StorageEntry) (globalConfig, bool, error) {
	var stored storedGlobalConfig
	if err := entry.DecodeJSON(&stored); err != nil {
		return globalConfig{}, false, err
	}
	cfg := globalConfig{
		Disabled:                     stored.Disabled,
		RestoreGuardAcknowledgedTime: stored.RestoreGuardAcknowledgedTime,
		UpdatedTime:                  stored.UpdatedTime,
	}
	changed := false
	if stored.RestoreGuard == nil {
		cfg.RestoreGuard = false
		changed = true
	} else {
		cfg.RestoreGuard = *stored.RestoreGuard
	}
	if stored.QueueCapacity == nil {
		cfg.QueueCapacity = defaultQueueCapacity
		changed = true
	} else {
		cfg.QueueCapacity = *stored.QueueCapacity
	}
	if stored.RequireSourceOptIn == nil {
		cfg.RequireSourceOptIn = false
		changed = true
	} else {
		cfg.RequireSourceOptIn = *stored.RequireSourceOptIn
	}
	if cfg.QueueCapacity < 0 {
		return globalConfig{}, false, fmt.Errorf("stored queue_capacity must be greater than or equal to zero")
	}
	return cfg, changed, nil
}

func initialGlobalConfig(now string) globalConfig {
	cfg := defaultGlobalConfig()
	cfg.RestoreGuardAcknowledgedTime = now
	cfg.UpdatedTime = now
	return cfg
}

func defaultGlobalConfig() globalConfig {
	return globalConfig{
		RestoreGuard:       false,
		QueueCapacity:      defaultQueueCapacity,
		RequireSourceOptIn: false,
	}
}

type storedGlobalConfig struct {
	Disabled                     bool   `json:"disabled"`
	RestoreGuard                 *bool  `json:"restore_guard"`
	RestoreGuardAcknowledgedTime string `json:"restore_guard_acknowledged_time"`
	QueueCapacity                *int   `json:"queue_capacity"`
	RequireSourceOptIn           *bool  `json:"require_source_opt_in"`
	UpdatedTime                  string `json:"updated_time"`
}

type globalConfig struct {
	Disabled                     bool   `json:"disabled"`
	RestoreGuard                 bool   `json:"restore_guard"`
	RestoreGuardAcknowledgedTime string `json:"restore_guard_acknowledged_time"`
	QueueCapacity                int    `json:"queue_capacity"`
	RequireSourceOptIn           bool   `json:"require_source_opt_in"`
	UpdatedTime                  string `json:"updated_time"`
}

const timeFormatRFC3339 = "2006-01-02T15:04:05Z07:00"
