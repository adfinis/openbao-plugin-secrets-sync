package backend

import (
	"context"
	"fmt"
	"time"

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
			"delegated_mode": {
				Type: framework.TypeBool,
				Description: "Require delegation guardrails for association-driven sync. " +
					"Requires require_source_opt_in=true and constrained destinations.",
			},
			"drift_repair": {
				Type: framework.TypeString,
				Description: "Background drift policy: off disables background drift work, detect records " +
					"periodic read-only drift status, repair also enqueues owned drift repair work.",
			},
			"drift_reconcile_interval": {
				Type:        framework.TypeString,
				Description: "Minimum interval between background drift checks for one association object.",
			},
			"drift_reconcile_batch": {
				Type:        framework.TypeInt,
				Description: "Maximum association objects checked by one periodic drift sweep.",
			},
			"event_dispatch_enabled": {
				Type:        framework.TypeBool,
				Description: "Wake a bounded dispatcher after enqueue-producing writes.",
			},
			"event_dispatch_max_operations": {
				Type:        framework.TypeInt,
				Description: "Maximum due outbox operations processed by one event-triggered dispatch wakeup.",
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
	return &logical.Response{Data: configResponse(state, cfg)}, nil
}

func configResponse(state runtimeState, cfg globalConfig) map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("disabled", cfg.Disabled),
		responseField("restore_guard", cfg.RestoreGuard),
		responseField("restore_guard_acknowledged_time", cfg.RestoreGuardAcknowledgedTime),
		responseField("restore_epoch", state.RestoreEpoch.Epoch),
		responseField("plugin_instance_id", state.PluginInstance.ID),
		responseField("storage_schema_version", state.Schema.Version),
		responseField("storage_schema_min_compatible_version", state.Schema.MinCompatibleVersion),
		responseField("queue_capacity", cfg.QueueCapacity),
		responseField("require_source_opt_in", cfg.RequireSourceOptIn),
		responseField("delegated_mode", cfg.DelegatedMode),
		responseField("drift_repair", cfg.DriftRepair),
		responseField("drift_reconcile_interval", cfg.DriftReconcileInterval),
		responseField("drift_reconcile_batch", cfg.DriftReconcileBatch),
		responseField("event_dispatch_enabled", cfg.EventDispatchEnabled),
		responseField("event_dispatch_max_operations", cfg.EventDispatchMaxOperations),
	)
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
	previousDisabled := cfg.Disabled
	previousRestoreGuard := cfg.RestoreGuard
	previousEventDispatchEnabled := cfg.EventDispatchEnabled
	if value, ok := data.GetOk("disabled"); ok {
		cfg.Disabled = value.(bool)
	}
	if value, ok := data.GetOk("restore_guard"); ok {
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
	if err := applyDelegationConfigFields(data, &cfg); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := applyDriftConfigFields(data, &cfg); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := applyEventDispatchConfigFields(data, &cfg); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	cfg.UpdatedTime = nowUTC().Format(timeFormatRFC3339)
	if err := putGlobalConfig(ctx, req.Storage, cfg); err != nil {
		return nil, err
	}
	b.observer.RestoreGuardActive(ctx, cfg.RestoreGuard)
	if configWriteResumesEventDispatch(
		previousDisabled,
		previousRestoreGuard,
		previousEventDispatchEnabled,
		cfg,
	) {
		b.signalEventDispatch()
	}
	return nil, nil
}

func applyDelegationConfigFields(data *framework.FieldData, cfg *globalConfig) error {
	if value, ok := data.GetOk("require_source_opt_in"); ok {
		cfg.RequireSourceOptIn = value.(bool)
	}
	if value, ok := data.GetOk("delegated_mode"); ok {
		cfg.DelegatedMode = value.(bool)
	}
	return validateDelegatedModeConfig(*cfg)
}

func applyDriftConfigFields(data *framework.FieldData, cfg *globalConfig) error {
	if value, ok := data.GetOk("drift_repair"); ok {
		driftRepair := value.(string)
		if err := validateDriftRepairMode(driftRepair); err != nil {
			return err
		}
		cfg.DriftRepair = driftRepair
	}
	if value, ok := data.GetOk("drift_reconcile_interval"); ok {
		interval := value.(string)
		if err := validateDriftReconcileInterval(interval); err != nil {
			return err
		}
		cfg.DriftReconcileInterval = interval
	}
	if value, ok := data.GetOk("drift_reconcile_batch"); ok {
		batch := value.(int)
		if batch < 1 {
			return fmt.Errorf("drift_reconcile_batch must be greater than or equal to one")
		}
		cfg.DriftReconcileBatch = batch
	}
	return nil
}

func applyEventDispatchConfigFields(data *framework.FieldData, cfg *globalConfig) error {
	if value, ok := data.GetOk("event_dispatch_enabled"); ok {
		cfg.EventDispatchEnabled = value.(bool)
	}
	if value, ok := data.GetOk("event_dispatch_max_operations"); ok {
		maxOperations := value.(int)
		if maxOperations < 1 {
			return fmt.Errorf("event_dispatch_max_operations must be greater than or equal to one")
		}
		cfg.EventDispatchMaxOperations = maxOperations
	}
	return nil
}

func configWriteResumesEventDispatch(
	previousDisabled bool,
	previousRestoreGuard bool,
	previousEventDispatchEnabled bool,
	cfg globalConfig,
) bool {
	if !cfg.EventDispatchEnabled || cfg.Disabled || cfg.RestoreGuard {
		return false
	}
	return previousDisabled || previousRestoreGuard || !previousEventDispatchEnabled
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
	b.signalEventDispatch()
	return &logical.Response{Data: configResponse(state, cfg)}, nil
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
	cfg.RestoreGuard, changed = optionalBoolConfigValue(stored.RestoreGuard, false, changed)
	if stored.QueueCapacity == nil {
		cfg.QueueCapacity = defaultQueueCapacity
		changed = true
	} else {
		cfg.QueueCapacity = *stored.QueueCapacity
	}
	cfg.RequireSourceOptIn, changed = optionalBoolConfigValue(stored.RequireSourceOptIn, false, changed)
	cfg.DelegatedMode, changed = optionalBoolConfigValue(stored.DelegatedMode, false, changed)
	if stored.DriftRepair == nil {
		cfg.DriftRepair = defaultDriftRepair
		changed = true
	} else {
		cfg.DriftRepair = *stored.DriftRepair
	}
	if stored.DriftReconcileInterval == nil {
		cfg.DriftReconcileInterval = defaultDriftInterval
		changed = true
	} else {
		cfg.DriftReconcileInterval = *stored.DriftReconcileInterval
	}
	if stored.DriftReconcileBatch == nil {
		cfg.DriftReconcileBatch = defaultDriftBatch
		changed = true
	} else {
		cfg.DriftReconcileBatch = *stored.DriftReconcileBatch
	}
	cfg.EventDispatchEnabled, changed = optionalBoolConfigValue(stored.EventDispatchEnabled, true, changed)
	if stored.EventDispatchMaxOperations == nil {
		cfg.EventDispatchMaxOperations = defaultEventDispatchMaxOperations
		changed = true
	} else {
		cfg.EventDispatchMaxOperations = *stored.EventDispatchMaxOperations
	}
	if cfg.QueueCapacity < 0 {
		return globalConfig{}, false, fmt.Errorf("stored queue_capacity must be greater than or equal to zero")
	}
	if err := validateDriftRepairMode(cfg.DriftRepair); err != nil {
		return globalConfig{}, false, fmt.Errorf("stored %w", err)
	}
	if err := validateDriftReconcileInterval(cfg.DriftReconcileInterval); err != nil {
		return globalConfig{}, false, fmt.Errorf("stored %w", err)
	}
	if cfg.DriftReconcileBatch < 1 {
		return globalConfig{}, false, fmt.Errorf("stored drift_reconcile_batch must be greater than or equal to one")
	}
	if cfg.EventDispatchMaxOperations < 1 {
		return globalConfig{}, false, fmt.Errorf("stored event_dispatch_max_operations must be greater than or equal to one")
	}
	if err := validateDelegatedModeConfig(cfg); err != nil {
		return globalConfig{}, false, fmt.Errorf("stored %w", err)
	}
	return cfg, changed, nil
}

func optionalBoolConfigValue(value *bool, fallback bool, changed bool) (bool, bool) {
	if value == nil {
		return fallback, true
	}
	return *value, changed
}

func initialGlobalConfig(now string) globalConfig {
	cfg := defaultGlobalConfig()
	cfg.RestoreGuardAcknowledgedTime = now
	cfg.UpdatedTime = now
	return cfg
}

func defaultGlobalConfig() globalConfig {
	return globalConfig{
		RestoreGuard:               false,
		QueueCapacity:              defaultQueueCapacity,
		RequireSourceOptIn:         false,
		DelegatedMode:              false,
		DriftRepair:                defaultDriftRepair,
		DriftReconcileInterval:     defaultDriftInterval,
		DriftReconcileBatch:        defaultDriftBatch,
		EventDispatchEnabled:       true,
		EventDispatchMaxOperations: defaultEventDispatchMaxOperations,
	}
}

type storedGlobalConfig struct {
	Disabled                     bool    `json:"disabled"`
	RestoreGuard                 *bool   `json:"restore_guard"`
	RestoreGuardAcknowledgedTime string  `json:"restore_guard_acknowledged_time"`
	QueueCapacity                *int    `json:"queue_capacity"`
	RequireSourceOptIn           *bool   `json:"require_source_opt_in"`
	DelegatedMode                *bool   `json:"delegated_mode"`
	DriftRepair                  *string `json:"drift_repair"`
	DriftReconcileInterval       *string `json:"drift_reconcile_interval"`
	DriftReconcileBatch          *int    `json:"drift_reconcile_batch"`
	EventDispatchEnabled         *bool   `json:"event_dispatch_enabled"`
	EventDispatchMaxOperations   *int    `json:"event_dispatch_max_operations"`
	UpdatedTime                  string  `json:"updated_time"`
}

type globalConfig struct {
	Disabled                     bool   `json:"disabled"`
	RestoreGuard                 bool   `json:"restore_guard"`
	RestoreGuardAcknowledgedTime string `json:"restore_guard_acknowledged_time"`
	QueueCapacity                int    `json:"queue_capacity"`
	RequireSourceOptIn           bool   `json:"require_source_opt_in"`
	DelegatedMode                bool   `json:"delegated_mode"`
	DriftRepair                  string `json:"drift_repair"`
	DriftReconcileInterval       string `json:"drift_reconcile_interval"`
	DriftReconcileBatch          int    `json:"drift_reconcile_batch"`
	EventDispatchEnabled         bool   `json:"event_dispatch_enabled"`
	EventDispatchMaxOperations   int    `json:"event_dispatch_max_operations"`
	UpdatedTime                  string `json:"updated_time"`
}

const timeFormatRFC3339 = "2006-01-02T15:04:05Z07:00"

func validateDelegatedModeConfig(cfg globalConfig) error {
	if cfg.DelegatedMode && !cfg.RequireSourceOptIn {
		return fmt.Errorf("delegated_mode requires require_source_opt_in=true")
	}
	return nil
}

func validateDriftRepairMode(mode string) error {
	switch mode {
	case driftRepairOff, driftRepairDetect, driftRepairRepair:
		return nil
	default:
		return fmt.Errorf("drift_repair must be one of off, detect, or repair")
	}
}

func validateDriftReconcileInterval(value string) error {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("drift_reconcile_interval must be a valid duration")
	}
	minimum, err := time.ParseDuration(minDriftInterval)
	if err != nil {
		return err
	}
	if duration < minimum {
		return fmt.Errorf("drift_reconcile_interval must be greater than or equal to %s", minDriftInterval)
	}
	return nil
}
