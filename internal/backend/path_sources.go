package backend

import (
	"context"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathSources(b *secretSyncBackend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "sources/(?P<path>.+)/check",
			Fields: map[string]*framework.FieldSchema{
				"path": {
					Type:        framework.TypeString,
					Description: "Source secret path.",
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ReadOperation: &framework.PathOperation{
					Callback: b.pathSourceCheck,
					Summary:  "Check source readiness.",
				},
			},
			HelpSynopsis:    "Check source sync readiness.",
			HelpDescription: "Reports whether a source path has a current version and, when required, is marked syncable.",
		},
		{
			Pattern: "sources/(?P<path>.+)/enable",
			Fields: map[string]*framework.FieldSchema{
				"path": {
					Type:        framework.TypeString,
					Description: "Source secret path.",
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathSourceEnable,
					Summary:  "Mark a source path as syncable.",
				},
			},
			HelpSynopsis:    "Enable source sync eligibility.",
			HelpDescription: "Marks a source path as explicitly syncable without requiring a custom_metadata JSON write.",
		},
	}
}

func (b *secretSyncBackend) pathSourceCheck(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	metadata, err := getMetadata(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	currentVersionAvailable := false
	if metadata != nil && metadata.CurrentVersion > 0 {
		version, err := getVersion(ctx, req.Storage, path, metadata.CurrentVersion)
		if err != nil {
			return nil, err
		}
		currentVersionAvailable = version != nil && !version.Destroyed && version.DeletionTime == ""
	}
	associations, err := listAssociationsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	enabledAssociations := 0
	for _, association := range associations {
		if association.Enabled {
			enabledAssociations++
		}
	}
	queuedOperations, err := listQueuedOutboxIDsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	statusRecords, err := listStatusRecordsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	cfg, err := readGlobalConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	currentVersion := 0
	syncable := false
	if metadata != nil {
		currentVersion = metadata.CurrentVersion
		syncable = sourceMetadataSyncable(*metadata)
	}
	blockers := sourceReadinessBlockers(metadata, syncable, currentVersionAvailable, cfg.RequireSourceOptIn)
	b.recordReadinessCheck(ctx, observability.CheckSource, "", blockers)
	return &logical.Response{Data: newResponseData(
		responseField("path", path),
		responseField("ready", len(blockers) == 0),
		responseField("syncable", syncable),
		responseField("source_opt_in_required", cfg.RequireSourceOptIn),
		responseField("current_version", currentVersion),
		responseField("current_version_available", currentVersionAvailable),
		responseField("association_count", len(associations)),
		responseField("enabled_association_count", enabledAssociations),
		responseField("queued_operations", len(queuedOperations)),
		responseField("status_objects", len(statusRecords)),
		responseField("blockers", blockers),
	)}, nil
}

func (b *secretSyncBackend) pathSourceEnable(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	unlock := b.lockSourcePath(path)
	defer unlock()

	metadata, err := getMetadata(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		metadata = newMetadataRecordPtr()
	}
	changed := !sourceMetadataSyncable(*metadata)
	if changed {
		metadata.CustomMetadata[sourceMetadataKeySyncable] = sourceMetadataValueTrue
		metadata.UpdatedTime = nowUTC().Format(timeFormatRFC3339)
		if err := putMetadata(ctx, req.Storage, path, *metadata); err != nil {
			return nil, err
		}
	}
	queuedOperations, err := listQueuedOutboxIDsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	statusRecords, err := listStatusRecordsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	return &logical.Response{Data: newResponseData(
		responseField("path", path),
		responseField("syncable", true),
		responseField("changed", changed),
		responseField("metadata", newResponseData(
			metadataResponseFields(*metadata, len(queuedOperations), len(statusRecords))...,
		)),
	)}, nil
}

func sourceMetadataSyncable(metadata metadataRecord) bool {
	return metadata.CustomMetadata[sourceMetadataKeySyncable] == sourceMetadataValueTrue
}

func sourceReadinessBlockers(
	metadata *metadataRecord,
	syncable bool,
	currentVersionAvailable bool,
	sourceOptInRequired bool,
) []string {
	blockers := []string{}
	if metadata == nil || metadata.CurrentVersion == 0 {
		blockers = append(blockers, "source_missing")
	} else if !currentVersionAvailable {
		blockers = append(blockers, "current_version_unavailable")
	}
	if sourceOptInRequired && !syncable {
		blockers = append(blockers, "source_not_syncable")
	}
	return blockers
}
