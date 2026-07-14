package backend

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/logical"
)

var (
	errQueuedOperationClaimed = errors.New("queued operation is currently claimed")
	errQueueCapacity          = errors.New("sync queue is full")
)

func isQueuedOperationClaimedError(err error) bool {
	return errors.Is(err, errQueuedOperationClaimed)
}

func putOutbox(ctx context.Context, storage logical.Storage, record outboxRecord) error {
	existing, err := getOutbox(ctx, storage, record.ID)
	if err != nil {
		return err
	}
	// Write replacement indexes before the canonical record. A failed index or
	// canonical write can then leave only stale indexes, which readers validate
	// against the authoritative canonical record.
	if err := putOutboxIndexes(ctx, storage, record); err != nil {
		return err
	}
	entry, err := logical.StorageEntryJSON(outboxStorageKey(record.ID), record)
	if err != nil {
		return err
	}
	if err := storage.Put(ctx, entry); err != nil {
		return err
	}
	if existing != nil {
		return deleteStaleOutboxIndexes(ctx, storage, *existing, record)
	}
	return nil
}

func putOutboxIndexes(ctx context.Context, storage logical.Storage, record outboxRecord) error {
	for _, key := range outboxIndexKeys(record) {
		entry, err := logical.StorageEntryJSON(key, record.ID)
		if err != nil {
			return err
		}
		if err := storage.Put(ctx, entry); err != nil {
			return err
		}
	}
	return nil
}

func outboxIndexKeys(record outboxRecord) []string {
	keys := []string{}
	if record.Path != "" {
		keys = append(keys, outboxByPathStorageKey(record.Path, record.ID))
	}
	if record.State != "" {
		keys = append(keys, outboxByStateStorageKey(record.State, record.ID))
	}
	if dueTime := outboxDueIndexTime(record); dueTime != "" {
		keys = append(keys, outboxByDueStorageKey(dueTime, record.ID))
	}
	return keys
}

func deleteStaleOutboxIndexes(
	ctx context.Context,
	storage logical.Storage,
	existing outboxRecord,
	updated outboxRecord,
) error {
	updatedKeys := make(map[string]struct{}, len(outboxIndexKeys(updated)))
	for _, key := range outboxIndexKeys(updated) {
		updatedKeys[key] = struct{}{}
	}
	for _, key := range outboxIndexKeys(existing) {
		if _, retained := updatedKeys[key]; retained {
			continue
		}
		if err := storage.Delete(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func deleteOutbox(ctx context.Context, storage logical.Storage, record outboxRecord) error {
	existing, err := getOutbox(ctx, storage, record.ID)
	if err != nil {
		return err
	}
	if err := storage.Delete(ctx, outboxStorageKey(record.ID)); err != nil {
		return err
	}
	if err := deleteOutboxIndexes(ctx, storage, record); err != nil {
		return err
	}
	if existing != nil {
		// The caller may hold an older copy whose state or due-time indexes differ
		// from storage. Delete both views so claimed/retried records do not leave a
		// stale queue index behind.
		return deleteOutboxIndexes(ctx, storage, *existing)
	}
	return nil
}

func deleteOutboxIndexes(ctx context.Context, storage logical.Storage, record outboxRecord) error {
	for _, key := range outboxIndexKeys(record) {
		if err := storage.Delete(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func getOutbox(ctx context.Context, storage logical.Storage, id string) (*outboxRecord, error) {
	entry, err := storage.Get(ctx, outboxStorageKey(id))
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var record outboxRecord
	if err := entry.DecodeJSON(&record); err != nil {
		return nil, err
	}
	normalizeOutboxDefaults(&record)
	return &record, nil
}

func normalizeOutboxDefaults(record *outboxRecord) {
	if record.Trigger == "" {
		record.Trigger = outboxTriggerUser
	}
}

func outboxTrigger(record outboxRecord) string {
	if record.Trigger == "" {
		return outboxTriggerUser
	}
	return record.Trigger
}

func listQueuedOutboxIDs(ctx context.Context, storage logical.Storage) ([]string, error) {
	return listOutboxIDsForStates(ctx, storage, outboxStatePending, outboxStateRetryWait)
}

func listOutboxIDsForState(ctx context.Context, storage logical.Storage, state string) ([]string, error) {
	ids, err := storage.List(ctx, outboxByStateStoragePrefix+state+"/")
	if err != nil {
		return nil, err
	}
	return filterOutboxIDs(ctx, storage, ids, func(record outboxRecord) bool {
		return record.State == state
	})
}

func listOutboxIDsForStates(ctx context.Context, storage logical.Storage, states ...string) ([]string, error) {
	ids := []string{}
	for _, state := range states {
		stateIDs, err := listOutboxIDsForState(ctx, storage, state)
		if err != nil {
			return nil, err
		}
		ids = append(ids, stateIDs...)
	}
	return ids, nil
}

func listDueOutboxIDs(ctx context.Context, storage logical.Storage, now time.Time) ([]string, error) {
	keys, err := logical.CollectKeysWithPrefix(ctx, storage, outboxByDueStoragePrefix)
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	ids := []string{}
	nowString := now.Format(timeFormatRFC3339)
	for _, key := range keys {
		trimmed := strings.TrimPrefix(key, outboxByDueStoragePrefix)
		dueTime, id, ok := strings.Cut(trimmed, "/")
		if !ok || id == "" {
			continue
		}
		if dueTime > nowString {
			break
		}
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil || outboxDueIndexTime(*record) != dueTime {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func nextFutureOutboxDueTime(ctx context.Context, storage logical.Storage, now time.Time) (*time.Time, error) {
	keys, err := logical.CollectKeysWithPrefix(ctx, storage, outboxByDueStoragePrefix)
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	nowString := now.Format(timeFormatRFC3339)
	for _, key := range keys {
		trimmed := strings.TrimPrefix(key, outboxByDueStoragePrefix)
		dueTime, id, ok := strings.Cut(trimmed, "/")
		if !ok || id == "" || dueTime <= nowString {
			continue
		}
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil || outboxDueIndexTime(*record) != dueTime {
			continue
		}
		next, err := time.Parse(timeFormatRFC3339, dueTime)
		if err != nil {
			continue
		}
		return &next, nil
	}
	return nil, nil
}

func outboxDueIndexTime(record outboxRecord) string {
	if !isQueuedOutboxState(record.State) {
		return ""
	}
	if _, err := time.Parse(timeFormatRFC3339, record.NotBefore); err == nil {
		return record.NotBefore
	}
	return outboxDueZeroTime
}

func listOutboxIDsForPath(ctx context.Context, storage logical.Storage, path string) ([]string, error) {
	ids, err := storage.List(ctx, outboxByPathStoragePrefix+path+"/")
	if err != nil {
		return nil, err
	}
	return filterOutboxIDs(ctx, storage, ids, func(record outboxRecord) bool {
		return record.Path == path
	})
}

func filterOutboxIDs(
	ctx context.Context,
	storage logical.Storage,
	ids []string,
	matches func(outboxRecord) bool,
) ([]string, error) {
	filtered := make([]string, 0, len(ids))
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil || !matches(*record) {
			continue
		}
		filtered = append(filtered, id)
	}
	return filtered, nil
}

func deleteOutboxForAssociation(
	ctx context.Context,
	storage logical.Storage,
	association associationRecord,
) error {
	ids, err := listOutboxIDsForPath(ctx, storage, association.Path)
	if err != nil {
		return err
	}
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return err
		}
		if record == nil || record.AssociationID != association.ID {
			continue
		}
		if isOutboxClaimActive(*record, nowUTC()) {
			return fmt.Errorf("%w: %s", errQueuedOperationClaimed, record.ID)
		}
		if err := deleteOutbox(ctx, storage, *record); err != nil {
			return err
		}
	}
	return nil
}

func (b *secretSyncBackend) pruneTerminalOutboxRecords(
	ctx context.Context,
	storage logical.Storage,
	now time.Time,
) error {
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()
	_, err := pruneTerminalOutboxRecordsLimit(
		ctx,
		storage,
		now,
		maxRetainedTerminalOutboxRecords,
		defaultTerminalOutboxPruneLimit,
	)
	return err
}

func pruneTerminalOutboxRecordsLimit(
	ctx context.Context,
	storage logical.Storage,
	now time.Time,
	maxRecords int,
	expiredLimit int,
) (int, error) {
	records, err := terminalOutboxRecords(ctx, storage)
	if err != nil {
		return 0, err
	}
	sort.Slice(records, func(i int, j int) bool {
		left := terminalOutboxRetentionTime(records[i])
		right := terminalOutboxRetentionTime(records[j])
		if left.Equal(right) {
			return records[i].ID < records[j].ID
		}
		return left.Before(right)
	})
	excess := max(len(records)-maxRecords, 0)
	cutoff := now.Add(-terminalOutboxRetention)
	deleted := 0
	expiredDeleted := 0
	for index, record := range records {
		retentionTime := terminalOutboxRetentionTime(record)
		expired := !retentionTime.IsZero() && !retentionTime.After(cutoff)
		if !shouldPruneTerminalOutboxRecord(index, excess, expired, expiredDeleted, expiredLimit) {
			continue
		}
		if err := deleteOutbox(ctx, storage, record); err != nil {
			return deleted, err
		}
		deleted++
		if expired && index >= excess {
			expiredDeleted++
		}
	}
	return deleted, nil
}

func terminalOutboxRecords(ctx context.Context, storage logical.Storage) ([]outboxRecord, error) {
	ids, err := listOutboxIDsForState(ctx, storage, outboxStateFailedTerminal)
	if err != nil {
		return nil, err
	}
	records := make([]outboxRecord, 0, len(ids))
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record != nil && record.State == outboxStateFailedTerminal {
			records = append(records, *record)
		}
	}
	return records, nil
}

func shouldPruneTerminalOutboxRecord(
	index int,
	excess int,
	expired bool,
	expiredDeleted int,
	expiredLimit int,
) bool {
	if index < excess {
		return true
	}
	if !expired {
		return false
	}
	return expiredLimit <= 0 || expiredDeleted < expiredLimit
}

func terminalOutboxRetentionTime(record outboxRecord) time.Time {
	for _, value := range []string{record.UpdatedTime, record.CreatedTime} {
		parsed, err := time.Parse(timeFormatRFC3339, value)
		if err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func cancelQueuedOutboxForAssociation(
	ctx context.Context,
	storage logical.Storage,
	association associationRecord,
) ([]string, error) {
	ids, err := listQueuedOutboxIDsForPath(ctx, storage, association.Path)
	if err != nil {
		return nil, err
	}
	canceledIDs := []string{}
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil || record.AssociationID != association.ID {
			continue
		}
		if isOutboxClaimActive(*record, nowUTC()) {
			return nil, fmt.Errorf("%w: %s", errQueuedOperationClaimed, record.ID)
		}
		if err := deleteOutbox(ctx, storage, *record); err != nil {
			return nil, err
		}
		canceledIDs = append(canceledIDs, record.ID)
	}
	return canceledIDs, nil
}

func queuedUpsertIDsForPathVersion(
	ctx context.Context,
	storage logical.Storage,
	path string,
	version int,
) ([]string, error) {
	ids, err := listQueuedOutboxIDsForPath(ctx, storage, path)
	if err != nil {
		return nil, err
	}
	matchingIDs := []string{}
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil || record.Version != version || record.Type != outbox.OperationTypeUpsert {
			continue
		}
		matchingIDs = append(matchingIDs, record.ID)
	}
	return matchingIDs, nil
}

func hasQueuedUpsertForAssociationObject(
	ctx context.Context,
	storage logical.Storage,
	path string,
	associationID string,
	objectID string,
	minVersion int,
) (bool, error) {
	ids, err := listQueuedOutboxIDsForPath(ctx, storage, path)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return false, err
		}
		if record == nil ||
			record.Type != outbox.OperationTypeUpsert ||
			record.AssociationID != associationID ||
			record.ObjectID != objectID {
			continue
		}
		if record.Version >= minVersion {
			return true, nil
		}
	}
	return false, nil
}

func hasQueuedUpsertForAssociationVersion(
	ctx context.Context,
	storage logical.Storage,
	path string,
	associationID string,
	minVersion int,
) (bool, error) {
	ids, err := listQueuedOutboxIDsForPath(ctx, storage, path)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return false, err
		}
		if record == nil ||
			record.Type != outbox.OperationTypeUpsert ||
			record.AssociationID != associationID {
			continue
		}
		if record.Version >= minVersion {
			return true, nil
		}
	}
	return false, nil
}

func cancelQueuedOutboxIDs(ctx context.Context, storage logical.Storage, ids []string) error {
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return err
		}
		if record == nil || !isQueuedOutboxState(record.State) {
			continue
		}
		if isOutboxClaimActive(*record, nowUTC()) {
			return fmt.Errorf("%w: %s", errQueuedOperationClaimed, record.ID)
		}
		if err := deleteOutbox(ctx, storage, *record); err != nil {
			return err
		}
	}
	return nil
}

func ensureQueuedOutboxIDsUnclaimed(ctx context.Context, storage logical.Storage, ids []string) error {
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return err
		}
		if record == nil || !isQueuedOutboxState(record.State) {
			continue
		}
		if isOutboxClaimActive(*record, nowUTC()) {
			return fmt.Errorf("%w: %s", errQueuedOperationClaimed, record.ID)
		}
	}
	return nil
}

func listQueuedOutboxIDsForPath(ctx context.Context, storage logical.Storage, path string) ([]string, error) {
	ids, err := listOutboxIDsForPath(ctx, storage, path)
	if err != nil {
		return nil, err
	}
	return filterQueuedOutboxIDs(ctx, storage, ids)
}

func filterQueuedOutboxIDs(ctx context.Context, storage logical.Storage, ids []string) ([]string, error) {
	queued := make([]string, 0, len(ids))
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil {
			continue
		}
		if isQueuedOutboxState(record.State) {
			queued = append(queued, id)
		}
	}
	return queued, nil
}

func isQueuedOutboxState(state string) bool {
	return state == outboxStatePending || state == outboxStateRetryWait
}
