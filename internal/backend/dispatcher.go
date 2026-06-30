package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/adfinis/openbao-secret-sync/internal/domain"
	"github.com/adfinis/openbao-secret-sync/internal/outbox"
	"github.com/adfinis/openbao-secret-sync/internal/providers"
	"github.com/adfinis/openbao-secret-sync/internal/providers/fake"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func processDueFakeOutbox(ctx context.Context, storage logical.Storage, now time.Time) error {
	ids, err := listOutboxIDs(ctx, storage)
	if err != nil {
		return err
	}
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return err
		}
		if record == nil || record.State != outboxStatePending {
			continue
		}
		if !isOutboxDue(*record, now) {
			continue
		}
		if !isFakeUpsertOperation(*record) {
			continue
		}
		if err := processFakeUpsert(ctx, storage, *record, now); err != nil {
			return err
		}
	}
	return nil
}

func isOutboxDue(record outboxRecord, now time.Time) bool {
	if record.NotBefore == "" {
		return true
	}
	notBefore, err := time.Parse(timeFormatRFC3339, record.NotBefore)
	if err != nil {
		return true
	}
	return !notBefore.After(now)
}

func isFakeUpsertOperation(record outboxRecord) bool {
	return record.Type == outbox.OperationTypeUpsert &&
		record.ObjectID == syncObjectIDSecretPath
}

func processFakeUpsert(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	now time.Time,
) error {
	version, err := getVersion(ctx, storage, record.Path, record.Version)
	if err != nil {
		return err
	}
	if version == nil {
		return markFakeOperationFailed(ctx, storage, record, "missing source version", now)
	}
	association, err := getAssociation(ctx, storage, record.Path, record.AssociationID)
	if err != nil {
		return err
	}
	if association == nil {
		return markFakeOperationFailed(ctx, storage, record, "missing association", now)
	}
	destination, err := getDestination(ctx, storage, association.DestinationType, association.DestinationName)
	if err != nil {
		return err
	}
	if destination == nil {
		return markFakeOperationFailed(ctx, storage, record, "missing destination", now)
	}
	if destination.Type != providerTypeFake {
		return markFakeOperationFailed(ctx, storage, record, "unsupported destination provider", now)
	}
	if destination.Disabled || !association.Enabled {
		return markFakeOperationFailed(ctx, storage, record, "association or destination disabled", now)
	}
	payload, err := json.Marshal(version.Data)
	if err != nil {
		return markFakeOperationFailed(ctx, storage, record, "source payload encoding failed", now)
	}

	result, err := fake.Provider{}.Upsert(ctx, providers.UpsertRequest{
		ResolvedName: association.ResolvedName,
		Payload:      payload,
	})
	if err != nil {
		return markFakeOperationFailed(ctx, storage, record, "provider upsert failed", now)
	}

	record.State = outboxStateSucceeded
	record.Attempts++
	record.UpdatedTime = now.Format(timeFormatRFC3339)
	if err := putOutbox(ctx, storage, record); err != nil {
		return err
	}
	return putStatus(ctx, storage, statusRecord{
		Path:            record.Path,
		Version:         record.Version,
		AssociationID:   record.AssociationID,
		ObjectID:        record.ObjectID,
		DestinationRef:  record.DestinationRef,
		ResolvedName:    association.ResolvedName,
		State:           string(domain.SyncStateSynced),
		RemoteVersion:   result.RemoteVersion,
		LastOperationID: record.ID,
		LastSuccessTime: now.Format(timeFormatRFC3339),
		UpdatedTime:     now.Format(timeFormatRFC3339),
	})
}

func markFakeOperationFailed(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	message string,
	now time.Time,
) error {
	record.State = outboxStateFailedTerminal
	record.Attempts++
	record.UpdatedTime = now.Format(timeFormatRFC3339)
	if err := putOutbox(ctx, storage, record); err != nil {
		return err
	}
	return putStatus(ctx, storage, statusRecord{
		Path:            record.Path,
		Version:         record.Version,
		AssociationID:   record.AssociationID,
		ObjectID:        record.ObjectID,
		DestinationRef:  record.DestinationRef,
		ResolvedName:    record.Path,
		State:           string(domain.SyncStateInternalError),
		LastOperationID: record.ID,
		LastErrorClass:  "internal_error",
		LastError:       fmt.Sprintf("fake dispatch failed: %s", message),
		UpdatedTime:     now.Format(timeFormatRFC3339),
	})
}
