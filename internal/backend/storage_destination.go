package backend

import (
	"context"
	"strings"

	"github.com/openbao/openbao/sdk/v2/logical"
)

func putDestination(ctx context.Context, storage logical.Storage, record destinationRecord) error {
	entry, err := logical.StorageEntryJSON(destinationStorageKey(record.Type, record.Name), record)
	if err != nil {
		return err
	}
	return storage.Put(ctx, entry)
}

func getDestination(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	name string,
) (*destinationRecord, error) {
	entry, err := storage.Get(ctx, destinationStorageKey(destinationType, name))
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var record destinationRecord
	if err := entry.DecodeJSON(&record); err != nil {
		return nil, err
	}
	normalizeDestinationDefaults(&record)
	return &record, nil
}

func normalizeDestinationDefaults(_ *destinationRecord) {
	// Destination records intentionally have no defaulted stored fields yet.
	// Keep this hook so future destination defaults are backfilled on read
	// instead of freezing zero-value behavior by accident.
}

func deleteDestination(ctx context.Context, storage logical.Storage, destinationType string, name string) error {
	return storage.Delete(ctx, destinationStorageKey(destinationType, name))
}

func listDestinationNamesPage(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	pagination listPagination,
) ([]string, error) {
	return storage.ListPage(ctx, destinationStoragePrefix+destinationType+"/", pagination.after, pagination.limit)
}

func putDestinationSensitiveConfig(
	ctx context.Context,
	storage logical.Storage,
	version string,
	record destinationSensitiveRecord,
) error {
	key := destinationSensitiveStorageKey(record.Type, record.Name)
	if version != "" {
		key = destinationSensitiveVersionStorageKey(record.Type, record.Name, version)
	}
	entry, err := logical.StorageEntryJSON(key, record)
	if err != nil {
		return err
	}
	return storage.Put(ctx, entry)
}

func getDestinationSensitiveConfig(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	name string,
) (*destinationSensitiveRecord, error) {
	record, err := getDestination(ctx, storage, destinationType, name)
	if err != nil || record == nil {
		return nil, err
	}
	return getDestinationSensitiveConfigForRecord(ctx, storage, *record)
}

func getDestinationSensitiveConfigForRecord(
	ctx context.Context,
	storage logical.Storage,
	record destinationRecord,
) (*destinationSensitiveRecord, error) {
	if record.SensitiveConfigVersion == destinationSensitiveNone {
		return nil, nil
	}
	key := destinationSensitiveStorageKey(record.Type, record.Name)
	if record.SensitiveConfigVersion != "" {
		key = destinationSensitiveVersionStorageKey(record.Type, record.Name, record.SensitiveConfigVersion)
	}
	entry, err := storage.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var sensitiveRecord destinationSensitiveRecord
	if err := entry.DecodeJSON(&sensitiveRecord); err != nil {
		return nil, err
	}
	return &sensitiveRecord, nil
}

func deleteDestinationSensitiveConfig(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	name string,
) error {
	if err := storage.Delete(ctx, destinationSensitiveStorageKey(destinationType, name)); err != nil {
		return err
	}
	prefix := destinationSensitiveVersionStoragePrefix(destinationType, name)
	versions, err := storage.List(ctx, prefix)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if strings.HasSuffix(version, "/") {
			continue
		}
		if err := storage.Delete(ctx, prefix+version); err != nil {
			return err
		}
	}
	return nil
}

func deleteDestinationSensitiveConfigVersion(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	name string,
	version string,
) error {
	if version == destinationSensitiveNone {
		return nil
	}
	if version == "" {
		return storage.Delete(ctx, destinationSensitiveStorageKey(destinationType, name))
	}
	return storage.Delete(ctx, destinationSensitiveVersionStorageKey(destinationType, name, version))
}
