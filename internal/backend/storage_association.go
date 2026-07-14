package backend

import (
	"context"

	"github.com/openbao/openbao/sdk/v2/logical"
)

func putAssociation(ctx context.Context, storage logical.Storage, record associationRecord) error {
	existing, err := getAssociation(ctx, storage, record.Path, record.ID)
	if err != nil {
		return err
	}
	entry, err := logical.StorageEntryJSON(associationStorageKey(record.Path, record.ID), record)
	if err != nil {
		return err
	}
	byDestinationEntry, err := logical.StorageEntryJSON(
		associationByDestinationStorageKey(record.DestinationType, record.DestinationName, record.ID),
		record.Path,
	)
	if err != nil {
		return err
	}
	reservationNames := record.reservationNames()
	reservationEntries := make([]*logical.StorageEntry, 0, len(reservationNames))
	for _, reservationName := range reservationNames {
		reservationEntry, err := logical.StorageEntryJSON(
			associationNameStorageKey(record.DestinationRef, reservationName, record.ID),
			record.Path,
		)
		if err != nil {
			return err
		}
		reservationEntries = append(reservationEntries, reservationEntry)
	}
	if err := storage.Put(ctx, byDestinationEntry); err != nil {
		return err
	}
	for _, reservationEntry := range reservationEntries {
		if err := storage.Put(ctx, reservationEntry); err != nil {
			return err
		}
	}
	// Write secondary indexes before the canonical association record. Readers
	// tolerate stale indexes, but the record must not become visible before its
	// destination and name-reservation indexes exist.
	if err := storage.Put(ctx, entry); err != nil {
		return err
	}
	if existing != nil {
		// Stale indexes are removed only after the replacement association is
		// visible, so association updates do not create a lookup gap.
		return deleteStaleAssociationIndexes(ctx, storage, *existing, record)
	}
	return nil
}

func getAssociation(ctx context.Context, storage logical.Storage, path string, id string) (*associationRecord, error) {
	entry, err := storage.Get(ctx, associationStorageKey(path, id))
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var record associationRecord
	if err := entry.DecodeJSON(&record); err != nil {
		return nil, err
	}
	normalizeAssociationDefaults(&record)
	return &record, nil
}

func normalizeAssociationDefaults(record *associationRecord) {
	if record.DeleteMode == "" {
		record.DeleteMode = defaultDeleteMode
	}
}

func deleteAssociation(ctx context.Context, storage logical.Storage, record associationRecord) error {
	if err := storage.Delete(ctx, associationStorageKey(record.Path, record.ID)); err != nil {
		return err
	}
	if err := storage.Delete(
		ctx,
		associationByDestinationStorageKey(record.DestinationType, record.DestinationName, record.ID),
	); err != nil {
		return err
	}
	for _, reservationName := range record.reservationNames() {
		key := associationNameStorageKey(record.DestinationRef, reservationName, record.ID)
		if err := storage.Delete(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func deleteStaleAssociationIndexes(
	ctx context.Context,
	storage logical.Storage,
	existing associationRecord,
	updated associationRecord,
) error {
	if existing.DestinationType != updated.DestinationType || existing.DestinationName != updated.DestinationName {
		if err := storage.Delete(
			ctx,
			associationByDestinationStorageKey(existing.DestinationType, existing.DestinationName, existing.ID),
		); err != nil {
			return err
		}
	}
	updatedReservationKeys := map[string]struct{}{}
	for _, reservationName := range updated.reservationNames() {
		updatedReservationKeys[associationNameStorageKey(updated.DestinationRef, reservationName, updated.ID)] = struct{}{}
	}
	for _, reservationName := range existing.reservationNames() {
		key := associationNameStorageKey(existing.DestinationRef, reservationName, existing.ID)
		if _, ok := updatedReservationKeys[key]; ok {
			continue
		}
		if err := storage.Delete(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func listAssociationIDsForPath(ctx context.Context, storage logical.Storage, path string) ([]string, error) {
	return storage.List(ctx, associationStoragePrefix+path+"/")
}

func listAssociationsForPath(ctx context.Context, storage logical.Storage, path string) ([]associationRecord, error) {
	ids, err := listAssociationIDsForPath(ctx, storage, path)
	if err != nil {
		return nil, err
	}
	records := make([]associationRecord, 0, len(ids))
	for _, id := range ids {
		record, err := getAssociation(ctx, storage, path, id)
		if err != nil {
			return nil, err
		}
		if record != nil {
			records = append(records, *record)
		}
	}
	return records, nil
}

func listAssociationIDsForDestination(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	name string,
) ([]string, error) {
	ids, err := storage.List(ctx, associationByDestPrefix+destinationRef(destinationType, name)+"/")
	if err != nil {
		return nil, err
	}
	liveIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		record, err := getAssociationFromIndex(
			ctx,
			storage,
			associationByDestinationStorageKey(destinationType, name, id),
			id,
		)
		if err != nil {
			return nil, err
		}
		if record == nil || record.DestinationType != destinationType || record.DestinationName != name {
			continue
		}
		liveIDs = append(liveIDs, id)
	}
	return liveIDs, nil
}

func listAssociationNameReservationIDs(
	ctx context.Context,
	storage logical.Storage,
	destinationReference string,
	resolvedName string,
) ([]string, error) {
	ids, err := storage.List(ctx, associationNamePrefix+destinationReference+"/"+nameReservationID(resolvedName)+"/")
	if err != nil {
		return nil, err
	}
	liveIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		record, err := getAssociationFromIndex(
			ctx,
			storage,
			associationNameStorageKey(destinationReference, resolvedName, id),
			id,
		)
		if err != nil {
			return nil, err
		}
		if record == nil || record.DestinationRef != destinationReference {
			continue
		}
		if associationReservesName(*record, resolvedName) {
			liveIDs = append(liveIDs, id)
		}
	}
	return liveIDs, nil
}

func getAssociationFromIndex(
	ctx context.Context,
	storage logical.Storage,
	indexKey string,
	id string,
) (*associationRecord, error) {
	entry, err := storage.Get(ctx, indexKey)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var path string
	if err := entry.DecodeJSON(&path); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	return getAssociation(ctx, storage, path, id)
}

func associationReservesName(record associationRecord, resolvedName string) bool {
	for _, reservationName := range record.reservationNames() {
		if reservationName == resolvedName {
			return true
		}
	}
	return false
}
