package backend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/logical"
)

type runtimeState struct {
	Schema         storageSchemaRecord
	PluginInstance pluginInstanceRecord
	RestoreEpoch   restoreEpochRecord
}

type storageSchemaCompatibilityError struct {
	message string
}

func (err storageSchemaCompatibilityError) Error() string {
	return "incompatible storage schema: " + err.message
}

func isStorageSchemaCompatibilityError(err error) bool {
	var schemaErr storageSchemaCompatibilityError
	return errors.As(err, &schemaErr)
}

func ensureRuntimeState(ctx context.Context, storage logical.Storage) (runtimeState, error) {
	now := nowUTC().Format(timeFormatRFC3339)
	schema, err := ensureStorageSchema(ctx, storage, now)
	if err != nil {
		return runtimeState{}, err
	}
	pluginInstance, err := ensurePluginInstance(ctx, storage, now)
	if err != nil {
		return runtimeState{}, err
	}
	restoreEpoch, err := ensureRestoreEpoch(ctx, storage, now)
	if err != nil {
		return runtimeState{}, err
	}
	return runtimeState{
		Schema:         schema,
		PluginInstance: pluginInstance,
		RestoreEpoch:   restoreEpoch,
	}, nil
}

func providerRuntimeIdentity(ctx context.Context, storage logical.Storage) (providers.RuntimeIdentity, error) {
	state, err := ensureRuntimeState(ctx, storage)
	if err != nil {
		return providers.RuntimeIdentity{}, err
	}
	return providers.RuntimeIdentity{
		PluginInstanceID: state.PluginInstance.ID,
		RestoreEpoch:     state.RestoreEpoch.Epoch,
	}, nil
}

func ensureStorageSchema(
	ctx context.Context,
	storage logical.Storage,
	now string,
) (storageSchemaRecord, error) {
	entry, err := storage.Get(ctx, storageSchemaKey)
	if err != nil {
		return storageSchemaRecord{}, err
	}
	if entry == nil {
		record := storageSchemaRecord{
			Version:              currentStorageSchema,
			MinCompatibleVersion: minSupportedStorageSchema,
			CreatedTime:          now,
			UpdatedTime:          now,
		}
		return record, putStorageSchema(ctx, storage, record)
	}
	var record storageSchemaRecord
	if err := entry.DecodeJSON(&record); err != nil {
		return storageSchemaRecord{}, err
	}
	if record.MinCompatibleVersion == 0 {
		record.MinCompatibleVersion = record.Version
	}
	return record, validateStorageSchema(record)
}

func validateStorageSchema(record storageSchemaRecord) error {
	if record.Version <= 0 {
		return storageSchemaCompatibilityError{message: "stored schema version must be greater than zero"}
	}
	if record.Version < minSupportedStorageSchema {
		return storageSchemaCompatibilityError{
			message: fmt.Sprintf(
				"stored schema version %d is older than minimum supported schema %d",
				record.Version,
				minSupportedStorageSchema,
			),
		}
	}
	if record.MinCompatibleVersion > currentStorageSchema {
		return storageSchemaCompatibilityError{
			message: fmt.Sprintf(
				"stored schema version %d requires plugin schema %d, but this binary supports schema %d",
				record.Version,
				record.MinCompatibleVersion,
				currentStorageSchema,
			),
		}
	}
	return nil
}

func putStorageSchema(ctx context.Context, storage logical.Storage, record storageSchemaRecord) error {
	entry, err := logical.StorageEntryJSON(storageSchemaKey, record)
	if err != nil {
		return err
	}
	return storage.Put(ctx, entry)
}

func ensurePluginInstance(
	ctx context.Context,
	storage logical.Storage,
	now string,
) (pluginInstanceRecord, error) {
	entry, err := storage.Get(ctx, pluginInstanceKey)
	if err != nil {
		return pluginInstanceRecord{}, err
	}
	if entry == nil {
		id, err := newRuntimeID("inst")
		if err != nil {
			return pluginInstanceRecord{}, err
		}
		record := pluginInstanceRecord{
			ID:          id,
			CreatedTime: now,
		}
		return record, putPluginInstance(ctx, storage, record)
	}
	var record pluginInstanceRecord
	if err := entry.DecodeJSON(&record); err != nil {
		return pluginInstanceRecord{}, err
	}
	if record.ID == "" {
		return pluginInstanceRecord{}, fmt.Errorf("plugin instance identity is empty")
	}
	return record, nil
}

func putPluginInstance(ctx context.Context, storage logical.Storage, record pluginInstanceRecord) error {
	entry, err := logical.StorageEntryJSON(pluginInstanceKey, record)
	if err != nil {
		return err
	}
	return storage.Put(ctx, entry)
}

func ensureRestoreEpoch(
	ctx context.Context,
	storage logical.Storage,
	now string,
) (restoreEpochRecord, error) {
	entry, err := storage.Get(ctx, restoreEpochKey)
	if err != nil {
		return restoreEpochRecord{}, err
	}
	if entry == nil {
		record, err := newRestoreEpochRecord(now)
		if err != nil {
			return restoreEpochRecord{}, err
		}
		return record, putRestoreEpoch(ctx, storage, record)
	}
	var record restoreEpochRecord
	if err := entry.DecodeJSON(&record); err != nil {
		return restoreEpochRecord{}, err
	}
	if record.Epoch == "" {
		return restoreEpochRecord{}, fmt.Errorf("restore epoch is empty")
	}
	return record, nil
}

func rotateRestoreEpoch(
	ctx context.Context,
	storage logical.Storage,
	now string,
) (restoreEpochRecord, error) {
	record, err := newRestoreEpochRecord(now)
	if err != nil {
		return restoreEpochRecord{}, err
	}
	return record, putRestoreEpoch(ctx, storage, record)
}

func putRestoreEpoch(ctx context.Context, storage logical.Storage, record restoreEpochRecord) error {
	entry, err := logical.StorageEntryJSON(restoreEpochKey, record)
	if err != nil {
		return err
	}
	return storage.Put(ctx, entry)
}

func newRestoreEpochRecord(now string) (restoreEpochRecord, error) {
	epoch, err := newRuntimeID("epoch")
	if err != nil {
		return restoreEpochRecord{}, err
	}
	return restoreEpochRecord{
		Epoch:       epoch,
		CreatedTime: now,
		UpdatedTime: now,
	}, nil
}

func newRuntimeID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(raw[:]), nil
}

func bestEffortRuntimeID(prefix string) string {
	id, err := newRuntimeID(prefix)
	if err != nil {
		return prefix + "-unavailable"
	}
	return id
}
