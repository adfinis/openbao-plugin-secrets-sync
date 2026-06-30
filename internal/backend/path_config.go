package backend

import (
	"context"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const configPath = "config"

func pathConfig(_ *secretSyncBackend) *framework.Path {
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
				Callback: pathConfigRead,
				Summary:  "Read global secret sync configuration.",
			},
			logical.UpdateOperation: &framework.PathOperation{
				Callback: pathConfigWrite,
				Summary:  "Update global secret sync configuration.",
			},
		},
		HelpSynopsis:    "Configure global secret sync behavior.",
		HelpDescription: "Controls mount-wide pause, restore guard, and queue capacity settings.",
	}
}

func pathConfigRead(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	entry, err := req.Storage.Get(ctx, configPath)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return &logical.Response{Data: newResponseData(
			responseField("disabled", false),
			responseField("restore_guard", true),
			responseField("queue_capacity", 1000),
		)}, nil
	}

	var cfg globalConfig
	if err := entry.DecodeJSON(&cfg); err != nil {
		return nil, err
	}
	return &logical.Response{Data: newResponseData(
		responseField("disabled", cfg.Disabled),
		responseField("restore_guard", cfg.RestoreGuard),
		responseField("queue_capacity", cfg.QueueCapacity),
	)}, nil
}

func pathConfigWrite(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	cfg := globalConfig{
		Disabled:      data.Get("disabled").(bool),
		RestoreGuard:  data.Get("restore_guard").(bool),
		QueueCapacity: data.Get("queue_capacity").(int),
		UpdatedTime:   nowUTC().Format(timeFormatRFC3339),
	}
	if cfg.QueueCapacity == 0 {
		cfg.QueueCapacity = 1000
	}
	entry, err := logical.StorageEntryJSON(configPath, cfg)
	if err != nil {
		return nil, err
	}
	if err := req.Storage.Put(ctx, entry); err != nil {
		return nil, err
	}
	return nil, nil
}

type globalConfig struct {
	Disabled      bool   `json:"disabled"`
	RestoreGuard  bool   `json:"restore_guard"`
	QueueCapacity int    `json:"queue_capacity"`
	UpdatedTime   string `json:"updated_time"`
}

const timeFormatRFC3339 = "2006-01-02T15:04:05Z07:00"
