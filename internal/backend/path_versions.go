package backend

import (
	"context"
	"fmt"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathVersionMutations(b *secretSyncBackend) []*framework.Path {
	fields := map[string]*framework.FieldSchema{
		"path": {
			Type:        framework.TypeString,
			Description: "Source secret path.",
		},
		"versions": {
			Type:        framework.TypeCommaIntSlice,
			Description: "Secret versions to mutate.",
		},
	}
	return []*framework.Path{
		{
			Pattern: "delete/" + framework.MatchAllRegex("path"),
			Fields:  fields,
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathDeleteVersionsWrite,
					Summary:  "Soft-delete local source secret versions.",
				},
			},
			HelpSynopsis:    "Delete local versions.",
			HelpDescription: "Sets deletion time on selected local source secret versions.",
		},
		{
			Pattern: "undelete/" + framework.MatchAllRegex("path"),
			Fields:  fields,
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathUndeleteWrite,
					Summary:  "Undelete local source secret versions.",
				},
			},
			HelpSynopsis:    "Undelete local versions.",
			HelpDescription: "Clears deletion time on selected local source secret versions.",
		},
		{
			Pattern: "destroy/" + framework.MatchAllRegex("path"),
			Fields:  fields,
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathDestroyWrite,
					Summary:  "Destroy local source secret versions.",
				},
			},
			HelpSynopsis:    "Destroy local versions.",
			HelpDescription: "Permanently removes payload data from selected local source secret versions.",
		},
	}
}

func (b *secretSyncBackend) pathDeleteVersionsWrite(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	return b.runVersionMutation(ctx, req, data, versionMutationDelete, softDeleteVersion)
}

func (b *secretSyncBackend) pathUndeleteWrite(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	return b.runVersionMutation(ctx, req, data, versionMutationUndelete, undeleteVersion)
}

func (b *secretSyncBackend) pathDestroyWrite(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	return b.runVersionMutation(ctx, req, data, versionMutationDestroy, destroyVersion)
}

type versionMutationKind string

const (
	versionMutationDelete   versionMutationKind = "delete"
	versionMutationUndelete versionMutationKind = "undelete"
	versionMutationDestroy  versionMutationKind = "destroy"
)

type versionMutationFunc func(
	context.Context,
	logical.Storage,
	*metadataRecord,
	string,
	int,
	string,
) error

func (b *secretSyncBackend) runVersionMutation(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
	kind versionMutationKind,
	mutate versionMutationFunc,
) (*logical.Response, error) {
	path, versions, err := versionMutationRequest(data)
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
		return nil, nil
	}
	now := nowUTC().Format(timeFormatRFC3339)
	signalDispatch := false
	for _, version := range versions {
		var mutationErr error
		if version == metadata.CurrentVersion {
			switch kind {
			case versionMutationDelete, versionMutationDestroy:
				mutationErr = b.mutateCurrentVersionDelete(ctx, req.Storage, metadata, path, version, now, mutate)
				signalDispatch = true
			case versionMutationUndelete:
				mutationErr = b.mutateCurrentVersionUndelete(ctx, req.Storage, metadata, path, version, now, mutate)
				signalDispatch = true
			default:
				mutationErr = mutate(ctx, req.Storage, metadata, path, version, now)
			}
		} else {
			mutationErr = mutate(ctx, req.Storage, metadata, path, version, now)
		}
		if mutationErr != nil {
			return errorResponseForOperationError(mutationErr, requestMountPath(req)), nil
		}
		if err := putMetadata(ctx, req.Storage, path, *metadata); err != nil {
			return nil, err
		}
	}
	if signalDispatch {
		b.signalEventDispatch()
	}
	return nil, nil
}

func (b *secretSyncBackend) mutateCurrentVersionDelete(
	ctx context.Context,
	storage logical.Storage,
	metadata *metadataRecord,
	path string,
	version int,
	now string,
	mutate versionMutationFunc,
) error {
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()

	deletePlan, err := buildSourceDeletePlan(ctx, storage, path, metadata.Generation, version, now)
	if err != nil {
		return err
	}
	if err := ensureQueueCapacityAfterReplacement(
		ctx,
		storage,
		deletePlan.additionalOperations,
		len(deletePlan.staleUpsertIDs),
	); err != nil {
		return err
	}
	if err := ensureQueuedOutboxIDsUnclaimed(ctx, storage, deletePlan.staleUpsertIDs); err != nil {
		return err
	}
	if err := putPendingEnqueueIntent(
		ctx,
		storage,
		path,
		metadata.Generation,
		version,
		deletePlan.operations,
		deletePlan.staleUpsertIDs,
		now,
	); err != nil {
		return err
	}
	if err := mutate(ctx, storage, metadata, path, version, now); err != nil {
		return err
	}
	if err := cancelQueuedOutboxIDs(ctx, storage, deletePlan.staleUpsertIDs); err != nil {
		return err
	}
	if err := putOutboxRecords(ctx, storage, deletePlan.operations); err != nil {
		return err
	}
	return completeEnqueueIntent(ctx, storage, path, version, deletePlan.operations, now)
}

func (b *secretSyncBackend) mutateCurrentVersionUndelete(
	ctx context.Context,
	storage logical.Storage,
	metadata *metadataRecord,
	path string,
	version int,
	now string,
	mutate versionMutationFunc,
) error {
	versionRecord, err := getVersion(ctx, storage, path, version)
	if err != nil {
		return err
	}
	if versionRecord == nil || versionRecord.Destroyed || versionRecord.DeletionTime == "" {
		return mutate(ctx, storage, metadata, path, version, now)
	}
	associations, err := enabledAssociationsForPath(ctx, storage, path)
	if err != nil {
		return err
	}
	operations, _, err := newAssociationOutboxRecords(
		associations,
		metadata.Generation,
		version,
		versionRecord.Data,
		now,
		associationOutboxOptions{
			operationType: outbox.OperationTypeUpsert,
			trigger:       outboxTriggerUser,
		},
	)
	if err != nil {
		return err
	}
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()

	staleDeleteIDs, err := queuedDeleteIDsForUpsertOperations(ctx, storage, operations)
	if err != nil {
		return err
	}
	additionalOperations, err := additionalQueuedOperationCount(ctx, storage, operations)
	if err != nil {
		return err
	}
	if err := ensureQueueCapacityAfterReplacement(ctx, storage, additionalOperations, len(staleDeleteIDs)); err != nil {
		return err
	}
	if err := ensureQueuedOutboxIDsUnclaimed(ctx, storage, staleDeleteIDs); err != nil {
		return err
	}
	if err := putPendingEnqueueIntent(
		ctx,
		storage,
		path,
		metadata.Generation,
		version,
		operations,
		staleDeleteIDs,
		now,
	); err != nil {
		return err
	}
	if err := mutate(ctx, storage, metadata, path, version, now); err != nil {
		return err
	}
	if err := cancelQueuedOutboxIDs(ctx, storage, staleDeleteIDs); err != nil {
		return err
	}
	if err := putOutboxRecords(ctx, storage, operations); err != nil {
		return err
	}
	return completeEnqueueIntent(ctx, storage, path, version, operations, now)
}

func versionMutationRequest(data *framework.FieldData) (string, []int, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return "", nil, err
	}
	versions := data.Get("versions").([]int)
	if len(versions) == 0 {
		return "", nil, fmt.Errorf("versions must contain at least one version")
	}
	for _, version := range versions {
		if version <= 0 {
			return "", nil, fmt.Errorf("versions must contain positive integers")
		}
	}
	return path, versions, nil
}

func softDeleteVersion(
	ctx context.Context,
	storage logical.Storage,
	metadata *metadataRecord,
	path string,
	version int,
	now string,
) error {
	record, err := getVersion(ctx, storage, path, version)
	if err != nil {
		return err
	}
	if record == nil || record.Destroyed || record.DeletionTime != "" {
		return nil
	}
	record.DeletionTime = now
	if err := putVersion(ctx, storage, path, *record); err != nil {
		return err
	}
	versionKey := versionMetadataKey(record.Version)
	versionMetadata := metadata.Versions[versionKey]
	versionMetadata.DeletionTime = now
	metadata.Versions[versionKey] = versionMetadata
	metadata.UpdatedTime = now
	return nil
}

func undeleteVersion(
	ctx context.Context,
	storage logical.Storage,
	metadata *metadataRecord,
	path string,
	version int,
	now string,
) error {
	record, err := getVersion(ctx, storage, path, version)
	if err != nil {
		return err
	}
	if record == nil || record.Destroyed {
		return nil
	}
	record.DeletionTime = ""
	if err := putVersion(ctx, storage, path, *record); err != nil {
		return err
	}
	versionKey := versionMetadataKey(version)
	versionMetadata := metadata.Versions[versionKey]
	versionMetadata.DeletionTime = ""
	metadata.Versions[versionKey] = versionMetadata
	metadata.UpdatedTime = now
	return nil
}

func destroyVersion(
	ctx context.Context,
	storage logical.Storage,
	metadata *metadataRecord,
	path string,
	version int,
	now string,
) error {
	record, err := getVersion(ctx, storage, path, version)
	if err != nil {
		return err
	}
	if record == nil || record.Destroyed {
		return nil
	}
	record.Data = nil
	record.Destroyed = true
	record.DeletionTime = ""
	if err := putVersion(ctx, storage, path, *record); err != nil {
		return err
	}
	versionKey := versionMetadataKey(version)
	versionMetadata := metadata.Versions[versionKey]
	versionMetadata.Destroyed = true
	versionMetadata.DeletionTime = ""
	metadata.Versions[versionKey] = versionMetadata
	metadata.UpdatedTime = now
	return nil
}
