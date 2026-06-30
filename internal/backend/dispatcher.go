package backend

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/adfinis/openbao-secret-sync/internal/domain"
	"github.com/adfinis/openbao-secret-sync/internal/outbox"
	payloadpkg "github.com/adfinis/openbao-secret-sync/internal/payload"
	"github.com/adfinis/openbao-secret-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	maxAutomaticRetryAttempts = 3
	retryBaseDelay            = 30 * time.Second
	retryMaxDelay             = 5 * time.Minute
)

func (b *secretSyncBackend) processDueOutbox(ctx context.Context, storage logical.Storage, now time.Time) error {
	ids, err := listOutboxIDs(ctx, storage)
	if err != nil {
		return err
	}
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return err
		}
		if record == nil || !isDispatchableOutboxState(record.State) {
			continue
		}
		if !isOutboxDue(*record, now) {
			continue
		}
		if !isSupportedOperation(*record) {
			continue
		}
		if err := b.processOutboxRecord(ctx, storage, *record, now); err != nil {
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

func isSupportedOperation(record outboxRecord) bool {
	if record.ObjectID != syncObjectIDSecretPath {
		return false
	}
	return record.Type == outbox.OperationTypeUpsert || record.Type == outbox.OperationTypeDelete
}

func isDispatchableOutboxState(state string) bool {
	return state == outboxStatePending || state == outboxStateRetryWait
}

func (b *secretSyncBackend) processOutboxRecord(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	now time.Time,
) error {
	switch record.Type {
	case outbox.OperationTypeUpsert:
		return b.processUpsert(ctx, storage, record, now)
	case outbox.OperationTypeDelete:
		return b.processDelete(ctx, storage, record, now)
	default:
		return nil
	}
}

func (b *secretSyncBackend) processUpsert(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	now time.Time,
) error {
	upsertContext, failure, err := b.loadUpsertContext(ctx, storage, record)
	if err != nil {
		return err
	}
	if failure != nil {
		return markOperationFailed(ctx, storage, record, *failure, now)
	}
	preparedPayload, failure := prepareProviderPayload(upsertContext)
	if failure != nil {
		return markOperationFailed(ctx, storage, record, *failure, now)
	}

	result, err := upsertContext.provider.Upsert(ctx, providers.UpsertRequest{
		Destination: providers.DestinationConfig{
			Name: upsertContext.destination.Name,
		},
		ResolvedName:  upsertContext.association.ResolvedName,
		Format:        preparedPayload.Format,
		Payload:       preparedPayload.Bytes,
		PayloadSHA256: preparedPayload.SHA256,
	})
	if err != nil {
		return markOperationFailed(ctx, storage, record, operationFailure{
			class:         providerErrorClass(err),
			message:       "provider upsert failed",
			resolvedName:  upsertContext.association.ResolvedName,
			payloadSHA256: preparedPayload.SHA256,
		}, now)
	}
	if result == nil {
		result = &providers.SyncResult{}
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
		ResolvedName:    upsertContext.association.ResolvedName,
		State:           string(domain.SyncStateSynced),
		PayloadSHA256:   preparedPayload.SHA256,
		RemoteVersion:   result.RemoteVersion,
		LastOperationID: record.ID,
		LastSuccessTime: now.Format(timeFormatRFC3339),
		UpdatedTime:     now.Format(timeFormatRFC3339),
	})
}

func (b *secretSyncBackend) processDelete(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	now time.Time,
) error {
	deleteContext, failure, err := b.loadDeleteContext(ctx, storage, record)
	if err != nil {
		return err
	}
	if failure != nil {
		return markOperationFailed(ctx, storage, record, *failure, now)
	}
	result, err := deleteContext.provider.Delete(ctx, providers.DeleteRequest{
		Destination: providers.DestinationConfig{
			Name: deleteContext.destination.Name,
		},
		ResolvedName:  deleteContext.association.ResolvedName,
		SourcePath:    record.Path,
		SourceVersion: record.Version,
	})
	if err != nil {
		return markOperationFailed(ctx, storage, record, operationFailure{
			class:        providerErrorClass(err),
			message:      "provider delete failed",
			resolvedName: deleteContext.association.ResolvedName,
		}, now)
	}
	if result == nil {
		result = &providers.SyncResult{}
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
		ResolvedName:    deleteContext.association.ResolvedName,
		State:           string(domain.SyncStateRemoteMissing),
		RemoteVersion:   result.RemoteVersion,
		LastOperationID: record.ID,
		LastSuccessTime: now.Format(timeFormatRFC3339),
		UpdatedTime:     now.Format(timeFormatRFC3339),
	})
}

type upsertContext struct {
	version     *versionRecord
	association *associationRecord
	destination *destinationRecord
	provider    providers.Provider
}

func (b *secretSyncBackend) loadUpsertContext(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
) (*upsertContext, *operationFailure, error) {
	version, err := getVersion(ctx, storage, record.Path, record.Version)
	if err != nil {
		return nil, nil, err
	}
	if version == nil || version.Destroyed || version.DeletionTime != "" {
		return nil, &operationFailure{class: providers.ErrorClassInternal, message: "source version is unavailable"}, nil
	}
	association, err := getAssociation(ctx, storage, record.Path, record.AssociationID)
	if err != nil {
		return nil, nil, err
	}
	if association == nil {
		return nil, &operationFailure{class: providers.ErrorClassInternal, message: "association is missing"}, nil
	}
	destination, err := getDestination(ctx, storage, association.DestinationType, association.DestinationName)
	if err != nil {
		return nil, nil, err
	}
	if destination == nil {
		return nil, &operationFailure{
			class:        providers.ErrorClassInternal,
			message:      "destination is missing",
			resolvedName: association.ResolvedName,
		}, nil
	}
	provider, err := b.providerRegistry.MustGet(destination.Type)
	if err != nil {
		return nil, &operationFailure{
			class:        providers.ErrorClassValidation,
			message:      "destination provider is unsupported",
			resolvedName: association.ResolvedName,
		}, nil
	}
	if destination.Disabled || !association.Enabled {
		return nil, &operationFailure{
			class:        providers.ErrorClassValidation,
			message:      "association or destination is disabled",
			resolvedName: association.ResolvedName,
		}, nil
	}
	return &upsertContext{
		version:     version,
		association: association,
		destination: destination,
		provider:    provider,
	}, nil, nil
}

type deleteContext struct {
	association *associationRecord
	destination *destinationRecord
	provider    providers.Provider
}

func (b *secretSyncBackend) loadDeleteContext(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
) (*deleteContext, *operationFailure, error) {
	version, err := getVersion(ctx, storage, record.Path, record.Version)
	if err != nil {
		return nil, nil, err
	}
	if version != nil && !version.Destroyed && version.DeletionTime == "" {
		return nil, &operationFailure{class: providers.ErrorClassValidation, message: "source version is not deleted"}, nil
	}
	association, err := getAssociation(ctx, storage, record.Path, record.AssociationID)
	if err != nil {
		return nil, nil, err
	}
	if association == nil {
		return nil, &operationFailure{class: providers.ErrorClassInternal, message: "association is missing"}, nil
	}
	if normalizedDeleteMode(association.DeleteMode) != deleteModeDelete {
		return nil, &operationFailure{
			class:        providers.ErrorClassValidation,
			message:      "association delete_mode does not permit remote delete",
			resolvedName: association.ResolvedName,
		}, nil
	}
	destination, err := getDestination(ctx, storage, association.DestinationType, association.DestinationName)
	if err != nil {
		return nil, nil, err
	}
	if destination == nil {
		return nil, &operationFailure{
			class:        providers.ErrorClassInternal,
			message:      "destination is missing",
			resolvedName: association.ResolvedName,
		}, nil
	}
	provider, err := b.providerRegistry.MustGet(destination.Type)
	if err != nil {
		return nil, &operationFailure{
			class:        providers.ErrorClassValidation,
			message:      "destination provider is unsupported",
			resolvedName: association.ResolvedName,
		}, nil
	}
	if destination.Disabled || !association.Enabled {
		return nil, &operationFailure{
			class:        providers.ErrorClassValidation,
			message:      "association or destination is disabled",
			resolvedName: association.ResolvedName,
		}, nil
	}
	return &deleteContext{
		association: association,
		destination: destination,
		provider:    provider,
	}, nil, nil
}

func prepareProviderPayload(upsertContext *upsertContext) (payloadpkg.CanonicalPayload, *operationFailure) {
	preparedPayload, err := buildCanonicalPayload(upsertContext.association.Format, upsertContext.version.Data)
	if err != nil {
		return payloadpkg.CanonicalPayload{}, &operationFailure{
			class:        providers.ErrorClassValidation,
			message:      "source payload encoding failed",
			resolvedName: upsertContext.association.ResolvedName,
		}
	}
	capabilities := upsertContext.provider.Capabilities()
	if err := enforceProviderPayloadLimit(capabilities, preparedPayload); err != nil {
		return payloadpkg.CanonicalPayload{}, &operationFailure{
			class:         providers.ErrorClassCapacity,
			message:       err.Error(),
			resolvedName:  upsertContext.association.ResolvedName,
			payloadSHA256: preparedPayload.SHA256,
		}
	}
	return preparedPayload, nil
}

func buildCanonicalPayload(format string, data secretPayload) (payloadpkg.CanonicalPayload, error) {
	switch format {
	case defaultAssociationFormat:
		return payloadpkg.BuildJSON(map[string]interface{}(data))
	default:
		return payloadpkg.CanonicalPayload{}, fmt.Errorf("unsupported payload format %q", format)
	}
}

func enforceProviderPayloadLimit(
	capabilities providers.Capabilities,
	payload payloadpkg.CanonicalPayload,
) error {
	if capabilities.MaxPayloadBytes <= 0 || len(payload.Bytes) <= capabilities.MaxPayloadBytes {
		return nil
	}
	return fmt.Errorf("payload exceeds provider max_payload_bytes %d", capabilities.MaxPayloadBytes)
}

func providerErrorClass(err error) providers.ErrorClass {
	var providerError *providers.Error
	if errors.As(err, &providerError) && providerError.Class != "" {
		return providerError.Class
	}
	return providers.ErrorClassInternal
}

type operationFailure struct {
	class         providers.ErrorClass
	message       string
	resolvedName  string
	payloadSHA256 string
}

func markOperationFailed(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	failure operationFailure,
	now time.Time,
) error {
	if failure.class == "" {
		failure.class = providers.ErrorClassInternal
	}
	if failure.resolvedName == "" {
		failure.resolvedName = record.Path
	}
	record.State = outboxStateFailedTerminal
	record.Attempts++
	if shouldRetryAutomatically(failure.class, record.Attempts) {
		record.State = outboxStateRetryWait
		record.NotBefore = now.Add(retryDelay(record.Attempts)).Format(timeFormatRFC3339)
	} else {
		record.NotBefore = ""
	}
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
		ResolvedName:    failure.resolvedName,
		State:           string(domain.SyncStateInternalError),
		PayloadSHA256:   failure.payloadSHA256,
		LastOperationID: record.ID,
		LastErrorClass:  string(failure.class),
		LastError:       fmt.Sprintf("dispatch failed: %s", failure.message),
		UpdatedTime:     now.Format(timeFormatRFC3339),
	})
}

func shouldRetryAutomatically(errorClass providers.ErrorClass, attempts int) bool {
	if attempts >= maxAutomaticRetryAttempts {
		return false
	}
	return errorClass == providers.ErrorClassRateLimit || errorClass == providers.ErrorClassUnavailable
}

func retryDelay(attempts int) time.Duration {
	if attempts <= 1 {
		return retryBaseDelay
	}
	delay := retryBaseDelay << (attempts - 1)
	if delay > retryMaxDelay {
		return retryMaxDelay
	}
	return delay
}
