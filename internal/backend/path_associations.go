package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/awssecretsmanager"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/gitlab"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const associationIDPattern = "assoc-[0-9a-f]{32}"

var (
	errSourcePathDoesNotExist          = errors.New("source path does not exist")
	errCurrentSourceVersionUnavailable = errors.New("current source version is unavailable")
)

var associationProviderConfigFieldKeys = []string{
	awssecretsmanager.ConfigKeyDeleteRecoveryWindowDays,
	gitlab.ConfigKeyEnvironmentScope,
	gitlab.ConfigKeyProtected,
	gitlab.ConfigKeyMasked,
	gitlab.ConfigKeyHidden,
	gitlab.ConfigKeyVariableRaw,
	gitlab.ConfigKeyVariableType,
}

var associationProviderConfigFieldKeysByType = map[string][]string{
	awssecretsmanager.ProviderType: {
		awssecretsmanager.ConfigKeyDeleteRecoveryWindowDays,
	},
	gitlab.ProviderType: {
		gitlab.ConfigKeyEnvironmentScope,
		gitlab.ConfigKeyProtected,
		gitlab.ConfigKeyMasked,
		gitlab.ConfigKeyHidden,
		gitlab.ConfigKeyVariableRaw,
		gitlab.ConfigKeyVariableType,
	},
}

var associationProviderIdentityFieldKeysByType = map[string][]string{
	gitlab.ProviderType: {gitlab.ConfigKeyEnvironmentScope},
}

func pathAssociations(b *secretSyncBackend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "associations/?",
			Fields:  paginationFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ListOperation: &framework.PathOperation{
					Callback: pathAssociationList,
					Summary:  "List association source path prefixes.",
				},
			},
			HelpSynopsis:    "List associations.",
			HelpDescription: "Lists configured association source path prefixes.",
		},
		{
			Pattern: "associations/(?P<path>.+)/plan",
			Fields:  associationRequestFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationPlan,
					Summary:  "Plan an association sync operation.",
				},
			},
			HelpSynopsis:    "Plan association sync.",
			HelpDescription: "Builds a non-mutating provider plan for the current source version.",
		},
		{
			Pattern: "associations/(?P<path>.+)/(?P<association_id>" + associationIDPattern + ")/disable",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationDisable,
					Summary:  "Disable an association.",
				},
			},
			HelpSynopsis:    "Disable an association.",
			HelpDescription: "Disables future enqueue and cancels queued work for one association.",
		},
		{
			Pattern: "associations/(?P<path>.+)/(?P<association_id>" + associationIDPattern + ")/enable",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationEnable,
					Summary:  "Enable an association.",
				},
			},
			HelpSynopsis:    "Enable an association.",
			HelpDescription: "Enables an association and enqueues the current source version when transitioning from disabled.",
		},
		{
			Pattern: "associations/(?P<path>.+)/(?P<association_id>" + associationIDPattern + ")/sync",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationSync,
					Summary:  "Manually enqueue association sync.",
				},
			},
			HelpSynopsis:    "Sync an association.",
			HelpDescription: "Enqueues the current source version for one enabled association.",
		},
		{
			Pattern: "associations/(?P<path>.+)/disable",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationDisable,
					Summary:  "Disable an association by destination.",
				},
			},
			HelpSynopsis:    "Disable an association.",
			HelpDescription: "Disables future enqueue and cancels queued work for one association resolved by destination.",
		},
		{
			Pattern: "associations/(?P<path>.+)/enable",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationEnable,
					Summary:  "Enable an association by destination.",
				},
			},
			HelpSynopsis: "Enable an association.",
			HelpDescription: "Enables an association resolved by destination and enqueues the current source version " +
				"when transitioning from disabled.",
		},
		{
			Pattern: "associations/(?P<path>.+)/sync",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationSync,
					Summary:  "Manually enqueue association sync by destination.",
				},
			},
			HelpSynopsis:    "Sync an association.",
			HelpDescription: "Enqueues the current source version for one enabled association resolved by destination.",
		},
		{
			Pattern: "associations/(?P<path>.+)/(?P<association_id>" + associationIDPattern + ")",
			Fields: map[string]*framework.FieldSchema{
				"path": {
					Type:        framework.TypeString,
					Description: "Source secret path.",
				},
				"association_id": {
					Type:        framework.TypeString,
					Description: "Association identifier.",
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ReadOperation: &framework.PathOperation{
					Callback: pathAssociationReadByID,
					Summary:  "Read one association.",
				},
				logical.DeleteOperation: &framework.PathOperation{
					Callback: b.pathAssociationDelete,
					Summary:  "Delete an association.",
				},
			},
			HelpSynopsis:    "Manage one association.",
			HelpDescription: "Reads or deletes one source-to-destination association.",
		},
		{
			Pattern: "associations/" + framework.MatchAllRegex("path"),
			Fields:  associationRequestFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.CreateOperation: &framework.PathOperation{
					Callback: b.pathAssociationWrite,
					Summary:  "Create an association.",
				},
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationWrite,
					Summary:  "Create or update an association.",
				},
				logical.ReadOperation: &framework.PathOperation{
					Callback: pathAssociationRead,
					Summary:  "Read associations for a source path.",
				},
			},
			HelpSynopsis:    "Manage associations.",
			HelpDescription: "Associates source secrets with configured destinations for asynchronous sync.",
		},
	}
}

func associationLifecycleFields() map[string]*framework.FieldSchema {
	return map[string]*framework.FieldSchema{
		"path": {
			Type:        framework.TypeString,
			Description: "Source secret path.",
		},
		"association_id": {
			Type:        framework.TypeString,
			Description: "Association identifier.",
		},
		"destination": {
			Type:        framework.TypeString,
			Description: "Destination reference in <type>/<name> form.",
		},
	}
}

func associationRequestFields() map[string]*framework.FieldSchema {
	return map[string]*framework.FieldSchema{
		"path": {
			Type:        framework.TypeString,
			Description: "Source secret path.",
		},
		"destination": {
			Type:        framework.TypeString,
			Description: "Destination reference in <type>/<name> form.",
		},
		"name_template": {
			Type:        framework.TypeString,
			Description: "Destination object name template.",
		},
		"resolved_name": {
			Type:        framework.TypeString,
			Description: "Explicit resolved destination object name.",
		},
		"granularity": {
			Type:        framework.TypeString,
			Description: "Sync granularity: secret-path or secret-key.",
		},
		"format": {
			Type:        framework.TypeString,
			Description: "Payload format: json or raw. Raw requires secret-key granularity.",
		},
		"data_mapping": {
			Type: framework.TypeString,
			Description: "Destination data mapping mode. payload stores the canonical payload as one destination value; " +
				"source-keys maps top-level source keys into destination-native data keys when supported.",
		},
		"data_key_template": {
			Type: framework.TypeString,
			Description: "Destination-native data key template for data_mapping source-keys. " +
				"Requires {{ key }} and defaults to {{ key }} when source-key data mapping is enabled.",
		},
		"delete_mode": {
			Type:        framework.TypeString,
			Description: "Remote delete behavior for this association: retain, delete, or orphan.",
		},
		awssecretsmanager.ConfigKeyDeleteRecoveryWindowDays: {
			Type: framework.TypeInt,
			Description: "AWS Secrets Manager scheduled-delete recovery window in days for this association. " +
				"Defaults to 7; AWS accepts 7 through 30.",
		},
		"enabled": {
			Type:        framework.TypeBool,
			Description: "Whether the association should enqueue sync work.",
		},
		gitlab.ConfigKeyEnvironmentScope: {
			Type:        framework.TypeString,
			Description: "GitLab variable environment scope. Defaults to * and participates in remote object identity.",
		},
		gitlab.ConfigKeyProtected: {
			Type:        framework.TypeBool,
			Description: "Whether GitLab variables produced by this association are protected.",
		},
		gitlab.ConfigKeyMasked: {
			Type:        framework.TypeBool,
			Description: "Whether GitLab variables produced by this association are masked.",
		},
		gitlab.ConfigKeyHidden: {
			Type:        framework.TypeBool,
			Description: "Whether GitLab variables are created as masked and hidden.",
		},
		gitlab.ConfigKeyVariableRaw: {
			Type:        framework.TypeBool,
			Description: "Whether GitLab variable reference expansion is disabled. Defaults to true.",
		},
		gitlab.ConfigKeyVariableType: {
			Type:        framework.TypeString,
			Description: "GitLab variable type: env_var or file.",
		},
	}
}

func (b *secretSyncBackend) pathAssociationDisable(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	record, unlock, response, err := b.associationFromLifecycleRequest(ctx, req, data)
	if unlock != nil {
		defer unlock()
	}
	if response != nil || err != nil {
		return response, err
	}
	if record == nil {
		return nil, nil
	}
	now := nowUTC().Format(timeFormatRFC3339)
	b.enqueueMu.Lock()
	canceledOperationIDs, err := cancelQueuedOutboxForAssociation(ctx, req.Storage, *record)
	b.enqueueMu.Unlock()
	if err != nil {
		if isQueuedOperationClaimedError(err) {
			return logical.ErrorResponse(err.Error()), nil
		}
		return nil, err
	}
	record.Enabled = false
	record.UpdatedTime = now
	if err := putAssociation(ctx, req.Storage, *record); err != nil {
		return nil, err
	}
	if err := markAssociationStatusDisabled(ctx, req.Storage, *record, now); err != nil {
		return nil, err
	}
	return &logical.Response{Data: associationLifecycleResponse(
		*record,
		responseField("canceled_operation_ids", canceledOperationIDs),
	)}, nil
}

func (b *secretSyncBackend) pathAssociationEnable(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	mount := requestMountPath(req)
	record, unlock, response, err := b.associationFromLifecycleRequest(ctx, req, data)
	if unlock != nil {
		defer unlock()
	}
	if response != nil || err != nil {
		return response, err
	}
	if record == nil {
		return nil, nil
	}
	metadata, response, err := metadataForAssociationActivation(ctx, req.Storage, *record)
	if response != nil || err != nil {
		return response, err
	}
	activationRecord, response, err := associationRecordForEnable(
		ctx,
		req.Storage,
		mount,
		*record,
		metadata,
	)
	if response != nil || err != nil {
		return response, err
	}
	previous := activationRecord
	wasEnabled := activationRecord.Enabled
	now := nowUTC().Format(timeFormatRFC3339)
	activationRecord.Enabled = true
	activationRecord.UpdatedTime = now
	if err := putAssociation(ctx, req.Storage, activationRecord); err != nil {
		return nil, err
	}
	operationIDs := []string{}
	if !wasEnabled {
		operationIDs, response, err = b.enqueueEnabledAssociationCurrentVersion(
			ctx,
			req.Storage,
			activationRecord,
			metadata,
			previous,
			mount,
			now,
		)
		if response != nil || err != nil {
			return response, err
		}
	}
	return &logical.Response{Data: associationLifecycleResponse(
		activationRecord,
		associationSyncLifecycleFields(mount, activationRecord, operationIDs, wasEnabled && len(operationIDs) == 0)...,
	)}, nil
}

func associationRecordForEnable(
	ctx context.Context,
	storage logical.Storage,
	mount string,
	record associationRecord,
	metadata *metadataRecord,
) (associationRecord, *logical.Response, error) {
	cfg, err := readGlobalConfig(ctx, storage)
	if err != nil {
		return associationRecord{}, nil, err
	}
	if err := validateSourceEligibility(metadata, cfg); err != nil {
		return associationRecord{}, errorResponseWithDiagnostic(
			err.Error(),
			validationDiagnosticForAssociation(mount, record, err.Error()),
		), nil
	}
	if err := validateAssociationDestination(ctx, storage, record, cfg); err != nil {
		return associationRecord{}, errorResponseWithDiagnostic(
			err.Error(),
			validationDiagnosticForAssociation(mount, record, err.Error()),
		), nil
	}
	version, err := currentVersionRecord(ctx, storage, record.Path, *metadata)
	if err != nil {
		if response := currentSourceVersionErrorResponse(err); response != nil {
			return associationRecord{}, response, nil
		}
		return associationRecord{}, nil, err
	}
	recordWithReservations, err := associationWithConcreteReservationNames(record, version.Data)
	if err != nil {
		return associationRecord{}, logical.ErrorResponse(err.Error()), nil
	}
	if err := validateAssociationNameReservations(
		ctx,
		storage,
		recordWithReservations.DestinationRef,
		recordWithReservations.reservationNames(),
		recordWithReservations.ID,
	); err != nil {
		return associationRecord{}, logical.ErrorResponse(err.Error()), nil
	}
	return recordWithReservations, nil, nil
}

func (b *secretSyncBackend) enqueueEnabledAssociationCurrentVersion(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	metadata *metadataRecord,
	previous associationRecord,
	mount string,
	now string,
) ([]string, *logical.Response, error) {
	operationIDs, err := b.enqueueAssociationCurrentVersion(ctx, storage, record, *metadata, now)
	if err != nil {
		if rollbackErr := rollbackAssociationPersistence(ctx, storage, record, &previous); rollbackErr != nil {
			return nil, nil, rollbackAssociationPersistenceError(err, rollbackErr)
		}
		return nil, errorResponseForOperationError(err, mount), nil
	}
	if len(operationIDs) > 0 {
		b.signalEventDispatch()
	}
	return operationIDs, nil, nil
}

func (b *secretSyncBackend) pathAssociationSync(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	mount := requestMountPath(req)
	record, unlock, response, err := b.associationFromLifecycleRequest(ctx, req, data)
	if unlock != nil {
		defer unlock()
	}
	if response != nil || err != nil {
		return response, err
	}
	if record == nil {
		return nil, nil
	}
	if !record.Enabled {
		return errorResponseWithDiagnostic("association is disabled", associationDisabledDiagnostic(mount, *record)), nil
	}
	metadata, response, err := metadataForAssociationActivation(ctx, req.Storage, *record)
	if response != nil || err != nil {
		return response, err
	}
	cfg, err := readGlobalConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if err := validateAssociationActivation(*record, metadata, cfg); err != nil {
		return errorResponseWithDiagnostic(err.Error(), validationDiagnosticForAssociation(mount, *record, err.Error())), nil
	}
	if err := validateAssociationDestination(ctx, req.Storage, *record, cfg); err != nil {
		return errorResponseWithDiagnostic(err.Error(), validationDiagnosticForAssociation(mount, *record, err.Error())), nil
	}
	now := nowUTC().Format(timeFormatRFC3339)
	operationIDs, err := b.enqueueAssociationCurrentVersionAsManualSync(ctx, req.Storage, *record, *metadata, now)
	if err != nil {
		return errorResponseForOperationError(err, mount), nil
	}
	if len(operationIDs) > 0 {
		b.signalEventDispatch()
	}
	return &logical.Response{Data: associationLifecycleResponse(
		*record,
		responseField("sync_operation_ids", operationIDs),
	)}, nil
}

func (b *secretSyncBackend) pathAssociationWrite(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	baseRecord, response, err := b.associationUpdateBase(ctx, req, path, data)
	if response != nil || err != nil {
		return response, err
	}
	record, err := b.associationRecordFromFieldData(ctx, req.Storage, path, data, baseRecord)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := validateAssociationIdentityUpdate(baseRecord, record); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	unlock := b.lockSourcePathAssociationNameAndDestination(path, record.DestinationRef, record.reservationName())
	defer unlock()

	mount := requestMountPath(req)
	plan, response, err := b.associationWritePlan(ctx, req.Storage, path, record, mount)
	if response != nil || err != nil {
		return response, err
	}

	return &logical.Response{Data: associationLifecycleResponse(
		plan.record,
		associationSyncLifecycleFields(
			mount,
			plan.record,
			plan.operationIDs,
			plan.existingWasEnabled && plan.record.Enabled && len(plan.operationIDs) == 0,
		)...,
	)}, nil
}

type associationWritePlan struct {
	record             associationRecord
	operationIDs       []string
	existingWasEnabled bool
}

func (b *secretSyncBackend) associationWritePlan(
	ctx context.Context,
	storage logical.Storage,
	path string,
	record associationRecord,
	mount string,
) (associationWritePlan, *logical.Response, error) {
	record, metadata, existing, response, err := associationWritePreflight(ctx, storage, path, record, mount)
	if response != nil || err != nil {
		return associationWritePlan{}, response, err
	}
	existingWasEnabled := existing != nil && existing.Enabled
	if existing != nil {
		record.CreatedTime = existing.CreatedTime
	}
	desiredStateChanged := existing != nil && associationDesiredStateChanged(*existing, record)
	shouldEnqueue := record.Enabled && (existing == nil || !existing.Enabled || desiredStateChanged)
	if err := putAssociation(ctx, storage, record); err != nil {
		return associationWritePlan{}, nil, err
	}
	operationIDs := []string{}
	if shouldEnqueue {
		now := nowUTC().Format(timeFormatRFC3339)
		if desiredStateChanged {
			operationIDs, err = b.enqueueAssociationCurrentVersionWithSalt(
				ctx,
				storage,
				record,
				*metadata,
				now,
				"association-config",
				false,
				outboxTriggerUser,
			)
		} else {
			operationIDs, err = b.enqueueAssociationCurrentVersion(ctx, storage, record, *metadata, now)
		}
		if err != nil {
			if rollbackErr := rollbackAssociationPersistence(ctx, storage, record, existing); rollbackErr != nil {
				return associationWritePlan{}, nil, rollbackAssociationPersistenceError(err, rollbackErr)
			}
			return associationWritePlan{}, errorResponseForOperationError(err, mount), nil
		}
		if len(operationIDs) > 0 {
			b.signalEventDispatch()
		}
	}
	return associationWritePlan{
		record:             record,
		operationIDs:       operationIDs,
		existingWasEnabled: existingWasEnabled,
	}, nil, nil
}

func associationDesiredStateChanged(existing associationRecord, updated associationRecord) bool {
	return existing.Format != updated.Format ||
		existing.DataMapping != updated.DataMapping ||
		existing.DataKeyTemplate != updated.DataKeyTemplate ||
		associationProviderDesiredStateChanged(existing, updated)
}

func associationProviderDesiredStateChanged(existing associationRecord, updated associationRecord) bool {
	ignoredKey := ""
	if existing.DestinationType == awssecretsmanager.ProviderType &&
		updated.DestinationType == awssecretsmanager.ProviderType {
		ignoredKey = awssecretsmanager.ConfigKeyDeleteRecoveryWindowDays
	}
	for key, existingValue := range existing.ProviderConfig {
		if key == ignoredKey {
			continue
		}
		if updated.ProviderConfig[key] != existingValue {
			return true
		}
	}
	for key := range updated.ProviderConfig {
		if key == ignoredKey {
			continue
		}
		if _, ok := existing.ProviderConfig[key]; !ok {
			return true
		}
	}
	return false
}

func stringMapsEqual(left map[string]string, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		if right[key] != leftValue {
			return false
		}
	}
	return true
}

func rollbackAssociationPersistence(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	previous *associationRecord,
) error {
	if previous != nil {
		return putAssociation(ctx, storage, *previous)
	}
	return deleteAssociation(ctx, storage, record)
}

func rollbackAssociationPersistenceError(operationErr error, rollbackErr error) error {
	return fmt.Errorf("%w; association rollback failed: %v", operationErr, rollbackErr)
}

func currentSourceVersion(
	ctx context.Context,
	storage logical.Storage,
	path string,
) (*metadataRecord, *versionRecord, error) {
	metadata, err := getMetadata(ctx, storage, path)
	if err != nil {
		return nil, nil, err
	}
	if metadata == nil || metadata.CurrentVersion == 0 {
		return nil, nil, errSourcePathDoesNotExist
	}
	version, err := currentVersionRecord(ctx, storage, path, *metadata)
	if err != nil {
		return nil, nil, err
	}
	return metadata, version, nil
}

func currentVersionRecord(
	ctx context.Context,
	storage logical.Storage,
	path string,
	metadata metadataRecord,
) (*versionRecord, error) {
	version, err := getVersion(ctx, storage, path, metadata.CurrentVersion)
	if err != nil {
		return nil, err
	}
	if version == nil || version.Destroyed || version.DeletionTime != "" {
		return nil, errCurrentSourceVersionUnavailable
	}
	return version, nil
}

func currentSourceVersionErrorResponse(err error) *logical.Response {
	if errors.Is(err, errSourcePathDoesNotExist) || errors.Is(err, errCurrentSourceVersionUnavailable) {
		return logical.ErrorResponse(err.Error())
	}
	return nil
}

func associationWritePreflight(
	ctx context.Context,
	storage logical.Storage,
	path string,
	record associationRecord,
	mount string,
) (associationRecord, *metadataRecord, *associationRecord, *logical.Response, error) {
	metadata, version, err := currentSourceVersion(ctx, storage, path)
	if err != nil {
		if response := currentSourceVersionErrorResponse(err); response != nil {
			return associationRecord{}, nil, nil, response, nil
		}
		return associationRecord{}, nil, nil, nil, err
	}
	record, err = associationWithConcreteReservationNames(record, version.Data)
	if err != nil {
		return associationRecord{}, nil, nil, logical.ErrorResponse(err.Error()), nil
	}
	if err := validateAssociationNameReservations(
		ctx,
		storage,
		record.DestinationRef,
		record.reservationNames(),
		record.ID,
	); err != nil {
		return associationRecord{}, nil, nil, logical.ErrorResponse(err.Error()), nil
	}
	cfg, err := readGlobalConfig(ctx, storage)
	if err != nil {
		return associationRecord{}, nil, nil, nil, err
	}
	if err := validateAssociationActivation(record, metadata, cfg); err != nil {
		return associationRecord{}, nil, nil, errorResponseWithDiagnostic(
			err.Error(),
			validationDiagnosticForAssociation(mount, record, err.Error()),
		), nil
	}
	if err := validateAssociationDestination(ctx, storage, record, cfg); err != nil {
		return associationRecord{}, nil, nil, errorResponseWithDiagnostic(
			err.Error(),
			validationDiagnosticForAssociation(mount, record, err.Error()),
		), nil
	}
	existing, err := getAssociation(ctx, storage, path, record.ID)
	if err != nil {
		return associationRecord{}, nil, nil, nil, err
	}
	return record, metadata, existing, nil, nil
}

func associationSummaryFields(record associationRecord) []responseEntry {
	return []responseEntry{
		responseField("association_id", record.ID),
		responseField("destination_ref", record.DestinationRef),
		responseField("resolved_name", record.ResolvedName),
		responseField("granularity", record.Granularity),
		responseField("format", record.Format),
		responseField("data_mapping", normalizedDataMapping(record.DataMapping)),
		responseField("data_key_template", record.DataKeyTemplate),
		responseField("provider_config", copyStringMap(record.ProviderConfig)),
		responseField("delete_mode", normalizedDeleteMode(record.DeleteMode)),
		responseField("enabled", record.Enabled),
	}
}

func providerAssociationConfig(record associationRecord) providers.AssociationConfig {
	return providers.AssociationConfig{
		Config:   copyStringMap(record.ProviderConfig),
		Identity: record.ProviderIdentity,
	}
}

func associationLifecycleResponse(record associationRecord, fields ...responseEntry) map[string]interface{} {
	return newResponseData(append(associationSummaryFields(record), fields...)...)
}

func associationSyncLifecycleFields(
	mount string,
	record associationRecord,
	operationIDs []string,
	includeManualSyncHint bool,
) []responseEntry {
	fields := []responseEntry{
		responseField("sync_operation_ids", operationIDs),
	}
	if includeManualSyncHint {
		fields = append(fields, diagnosticResponseFields(associationAlreadyEnabledDiagnostic(mount, record))...)
	}
	return fields
}

func pathAssociationRead(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	records, err := listAssociationsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	associations := make([]map[string]interface{}, 0, len(records)) //nolint:forbidigo
	for _, record := range records {
		associations = append(associations, associationResponse(record))
	}
	return &logical.Response{Data: newResponseData(
		responseField("path", path),
		responseField("associations", associations),
	)}, nil
}

func pathAssociationReadByID(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	record, err := getAssociation(ctx, req.Storage, path, data.Get("association_id").(string))
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	return &logical.Response{Data: associationResponse(*record)}, nil
}

func pathAssociationList(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	pagination := listPaginationFromFieldData(data)
	keys, err := req.Storage.ListPage(ctx, associationStoragePrefix, pagination.after, pagination.limit)
	if err != nil {
		return nil, err
	}
	return logical.ListResponse(keys), nil
}

func (b *secretSyncBackend) pathAssociationDelete(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	record, err := getAssociation(ctx, req.Storage, path, data.Get("association_id").(string))
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	unlock := b.lockSourcePathAndAssociationName(path, record.DestinationRef, record.reservationName())
	defer unlock()

	record, err = getAssociation(ctx, req.Storage, path, data.Get("association_id").(string))
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	b.enqueueMu.Lock()
	if err := deleteOutboxForAssociation(ctx, req.Storage, *record); err != nil {
		b.enqueueMu.Unlock()
		if isQueuedOperationClaimedError(err) {
			return logical.ErrorResponse(err.Error()), nil
		}
		return nil, err
	}
	b.enqueueMu.Unlock()
	if err := deleteAssociation(ctx, req.Storage, *record); err != nil {
		return nil, err
	}
	return nil, nil
}

func (b *secretSyncBackend) associationFromLifecycleRequest(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*associationRecord, func(), *logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return nil, nil, logical.ErrorResponse(err.Error()), nil
	}
	unlock := b.lockSourcePath(path)
	associationID := strings.TrimSpace(data.Get("association_id").(string))
	if associationID == "" {
		destinationType, destinationName, err := associationDestinationFromFieldData(data, true)
		if err != nil {
			unlock()
			return nil, nil, logical.ErrorResponse(err.Error()), nil
		}
		record, response, err := associationForDestination(ctx, req.Storage, path, destinationType, destinationName)
		if response != nil || err != nil {
			unlock()
			return nil, nil, response, err
		}
		return record, unlock, nil, nil
	}
	record, err := getAssociation(ctx, req.Storage, path, associationID)
	if err != nil {
		unlock()
		return nil, nil, nil, err
	}
	if record == nil {
		unlock()
		return nil, nil, nil, nil
	}
	return record, unlock, nil, nil
}

func associationForDestination(
	ctx context.Context,
	storage logical.Storage,
	path string,
	destinationType string,
	destinationName string,
) (*associationRecord, *logical.Response, error) {
	records, err := listAssociationsForPath(ctx, storage, path)
	if err != nil {
		return nil, nil, err
	}
	var match *associationRecord
	for i := range records {
		if records[i].DestinationType != destinationType || records[i].DestinationName != destinationName {
			continue
		}
		if match != nil {
			return nil, logical.ErrorResponse(
				"association request is ambiguous for destination %s/%s; use the association_id path",
				destinationType,
				destinationName,
			), nil
		}
		match = &records[i]
	}
	if match == nil {
		return nil, logical.ErrorResponse(
			"association for destination %s/%s does not exist at source path %q",
			destinationType,
			destinationName,
			path,
		), nil
	}
	return match, nil, nil
}

func (b *secretSyncBackend) associationRecordFromFieldData(
	ctx context.Context,
	storage logical.Storage,
	path string,
	data *framework.FieldData,
	base *associationRecord,
) (associationRecord, error) {
	destinationType, destinationName, err := associationDestinationFromFieldData(data, true)
	if err != nil {
		return associationRecord{}, err
	}
	provider, destination, destinationConfig, err := b.associationProvider(
		ctx,
		storage,
		destinationType,
		destinationName,
	)
	if err != nil {
		return associationRecord{}, err
	}
	providerConfig, err := associationProviderConfigFromFieldData(destinationType, base, data)
	if err != nil {
		return associationRecord{}, err
	}
	normalizedProviderConfig, err := provider.NormalizeAssociationConfig(
		ctx,
		destinationConfig,
		providers.AssociationConfig{Config: providerConfig},
	)
	if err != nil {
		return associationRecord{}, err
	}
	if destination.Type != destinationType || destination.Name != destinationName {
		return associationRecord{}, fmt.Errorf("destination identity changed during association validation")
	}

	granularity := stringFromField(data, "granularity", associationGranularityDefault(base))
	baseMatchesGranularity := base != nil && base.Granularity == granularity
	format := stringFromField(data, "format", associationFormatDefault(base, baseMatchesGranularity))
	dataMapping := stringFromField(
		data,
		"data_mapping",
		associationDataMappingDefault(base, baseMatchesGranularity),
	)
	dataKeyTemplate := stringFromField(
		data,
		"data_key_template",
		associationDataKeyTemplateDefault(base, dataMapping, baseMatchesGranularity),
	)
	deleteMode := stringFromField(data, "delete_mode", associationDeleteModeDefault(base))
	nameTemplate := stringFromField(
		data,
		"name_template",
		associationNameTemplateDefault(base, granularity, baseMatchesGranularity),
	)
	resolvedName := stringFromField(data, "resolved_name", associationResolvedNameDefault(base, baseMatchesGranularity))
	resolvedName, reservationName, err := resolveAssociationNames(
		path,
		destinationType,
		destinationName,
		granularity,
		nameTemplate,
		resolvedName,
	)
	if err != nil {
		return associationRecord{}, err
	}
	if err := validateAssociationCapabilities(provider.Capabilities(), granularity, format); err != nil {
		return associationRecord{}, err
	}
	if err := validateAssociationDataMapping(
		provider.Capabilities(),
		granularity,
		format,
		dataMapping,
		dataKeyTemplate,
	); err != nil {
		return associationRecord{}, err
	}
	if err := validateDeleteMode(provider.Capabilities(), deleteMode); err != nil {
		return associationRecord{}, err
	}

	id := newAssociationID(
		path,
		destinationType,
		destinationName,
		associationReservationKey(normalizedProviderConfig.Identity, reservationName),
		granularity,
	)
	destinationReference := destinationRef(destinationType, destinationName)

	now := nowUTC().Format(timeFormatRFC3339)
	return associationRecord{
		ID:               id,
		Path:             path,
		DestinationType:  destinationType,
		DestinationName:  destinationName,
		DestinationRef:   destinationReference,
		NameTemplate:     nameTemplate,
		ResolvedName:     resolvedName,
		Granularity:      granularity,
		Format:           format,
		DataMapping:      dataMapping,
		DataKeyTemplate:  dataKeyTemplate,
		ProviderConfig:   copyStringMap(normalizedProviderConfig.Config),
		ProviderIdentity: normalizedProviderConfig.Identity,
		DeleteMode:       deleteMode,
		Enabled:          boolFromField(data, "enabled", associationEnabledDefault(base)),
		CreatedTime:      now,
		UpdatedTime:      now,
	}, nil
}

func validateAssociationIdentityUpdate(base *associationRecord, record associationRecord) error {
	if base == nil {
		return nil
	}
	if record.Granularity != base.Granularity {
		return associationIdentityChangeError("granularity", base.ID)
	}
	if record.ProviderIdentity != base.ProviderIdentity {
		return associationIdentityChangeError(associationProviderIdentityField(*base, record), base.ID)
	}
	if record.reservationName() != base.reservationName() {
		return associationIdentityChangeError(associationReservationIdentityField(*base), base.ID)
	}
	return nil
}

func associationProviderIdentityField(base associationRecord, updated associationRecord) string {
	for _, field := range associationProviderIdentityFieldKeysByType[updated.DestinationType] {
		if base.ProviderConfig[field] != updated.ProviderConfig[field] {
			return field
		}
	}
	return "provider configuration"
}

func associationReservationIdentityField(record associationRecord) string {
	if record.Granularity == syncGranularitySecretKey {
		return "name_template"
	}
	return "resolved_name"
}

func associationIdentityChangeError(field string, associationID string) error {
	return fmt.Errorf(
		"%s change would create a new association identity; delete %s first, "+
			"then create the new association explicitly",
		field,
		associationID,
	)
}

func (b *secretSyncBackend) associationUpdateBase(
	ctx context.Context,
	req *logical.Request,
	path string,
	data *framework.FieldData,
) (*associationRecord, *logical.Response, error) {
	if req.Operation != logical.UpdateOperation {
		return nil, nil, nil
	}
	destinationType, destinationName, err := associationDestinationFromFieldData(data, false)
	if err != nil {
		return nil, logical.ErrorResponse(err.Error()), nil
	}
	if destinationType == "" || destinationName == "" {
		return nil, nil, nil
	}
	providerIdentity, identitySpecified, err := b.associationProviderIdentitySelector(
		ctx,
		req.Storage,
		destinationType,
		destinationName,
		data,
	)
	if err != nil {
		return nil, logical.ErrorResponse(err.Error()), nil
	}
	records, err := listAssociationsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, nil, err
	}
	var match *associationRecord
	for i := range records {
		if records[i].DestinationType != destinationType || records[i].DestinationName != destinationName {
			continue
		}
		if identitySpecified && records[i].ProviderIdentity != providerIdentity {
			continue
		}
		if match != nil {
			return nil, logical.ErrorResponse(
				"association update is ambiguous for destination %s/%s; "+
					"include provider identity fields or address one association explicitly",
				destinationType,
				destinationName,
			), nil
		}
		match = &records[i]
	}
	return match, nil, nil
}

func (b *secretSyncBackend) associationProviderIdentitySelector(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	destinationName string,
	data *framework.FieldData,
) (string, bool, error) {
	identitySpecified := false
	for _, field := range associationProviderIdentityFieldKeysByType[destinationType] {
		if _, ok := data.Raw[field]; ok {
			identitySpecified = true
			break
		}
	}
	if !identitySpecified {
		return "", false, nil
	}
	provider, _, destinationConfig, err := b.associationProvider(
		ctx,
		storage,
		destinationType,
		destinationName,
	)
	if err != nil {
		return "", false, err
	}
	providerConfig, err := associationProviderConfigFromFieldData(destinationType, nil, data)
	if err != nil {
		return "", false, err
	}
	normalized, err := provider.NormalizeAssociationConfig(
		ctx,
		destinationConfig,
		providers.AssociationConfig{Config: providerConfig},
	)
	if err != nil {
		return "", false, err
	}
	return normalized.Identity, true, nil
}

func associationDestinationFromFieldData(
	data *framework.FieldData,
	required bool,
) (string, string, error) {
	destination := strings.TrimSpace(data.Get("destination").(string))
	if destination == "" {
		if required {
			return "", "", fmt.Errorf("destination is required")
		}
		return "", "", nil
	}
	return parseDestinationRef(destination)
}

func parseDestinationRef(destination string) (string, string, error) {
	parts := strings.Split(destination, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("destination must be in <type>/<name> form")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func associationGranularityDefault(base *associationRecord) string {
	if base != nil && base.Granularity != "" {
		return base.Granularity
	}
	return syncGranularitySecretPath
}

func associationFormatDefault(base *associationRecord, baseMatchesGranularity bool) string {
	if baseMatchesGranularity && base.Format != "" {
		return base.Format
	}
	return defaultAssociationFormat
}

func associationDataMappingDefault(base *associationRecord, baseMatchesGranularity bool) string {
	if baseMatchesGranularity && base.DataMapping != "" {
		return base.DataMapping
	}
	return defaultDataMapping
}

func associationDataKeyTemplateDefault(
	base *associationRecord,
	dataMapping string,
	baseMatchesGranularity bool,
) string {
	if dataMapping != dataMappingSourceKeys {
		return ""
	}
	if baseMatchesGranularity && base.DataMapping == dataMappingSourceKeys && base.DataKeyTemplate != "" {
		return base.DataKeyTemplate
	}
	return defaultDataKeyTemplate
}

func associationDeleteModeDefault(base *associationRecord) string {
	if base != nil && base.DeleteMode != "" {
		return base.DeleteMode
	}
	return defaultDeleteMode
}

func associationNameTemplateDefault(
	base *associationRecord,
	granularity string,
	baseMatchesGranularity bool,
) string {
	if baseMatchesGranularity && base.NameTemplate != "" {
		return base.NameTemplate
	}
	return defaultNameTemplateForGranularity(granularity)
}

func associationResolvedNameDefault(base *associationRecord, baseMatchesGranularity bool) string {
	if baseMatchesGranularity {
		return base.ResolvedName
	}
	return ""
}

func associationEnabledDefault(base *associationRecord) bool {
	if base != nil {
		return base.Enabled
	}
	return true
}

func (b *secretSyncBackend) associationProvider(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	destinationName string,
) (providers.Provider, destinationRecord, providers.DestinationConfig, error) {
	provider, err := b.providerRegistry.MustGet(destinationType)
	if err != nil {
		return nil, destinationRecord{}, providers.DestinationConfig{}, err
	}
	destination, err := getDestination(ctx, storage, destinationType, destinationName)
	if err != nil {
		return nil, destinationRecord{}, providers.DestinationConfig{}, err
	}
	if destination == nil {
		return nil, destinationRecord{}, providers.DestinationConfig{}, fmt.Errorf(
			"destination %s/%s does not exist",
			destinationType,
			destinationName,
		)
	}
	if destination.Disabled {
		return nil, destinationRecord{}, providers.DestinationConfig{}, fmt.Errorf(
			"destination %s/%s is disabled",
			destinationType,
			destinationName,
		)
	}
	resolvedConfig, err := destinationConfig(ctx, storage, *destination)
	if err != nil {
		return nil, destinationRecord{}, providers.DestinationConfig{}, err
	}
	return provider, *destination, resolvedConfig, nil
}

func associationProviderConfigFromFieldData(
	destinationType string,
	base *associationRecord,
	data *framework.FieldData,
) (map[string]string, error) {
	var existingConfig map[string]string
	if base != nil {
		existingConfig = base.ProviderConfig
	}
	return providerConfigMapFromFieldData(
		destinationType,
		existingConfig,
		associationProviderConfigFieldKeysByType[destinationType],
		associationProviderConfigFieldKeys,
		data,
	)
}

func resolveAssociationNames(
	path string,
	destinationType string,
	destinationName string,
	granularity string,
	nameTemplate string,
	resolvedName string,
) (string, string, error) {
	switch granularity {
	case syncGranularitySecretPath:
		resolvedName, err := resolveSecretPathAssociationName(
			path,
			destinationType,
			destinationName,
			nameTemplate,
			resolvedName,
		)
		if err != nil {
			return "", "", err
		}
		return resolvedName, resolvedName, nil
	case syncGranularitySecretKey:
		if err := validateSecretKeyAssociationTemplate(
			path,
			destinationType,
			destinationName,
			nameTemplate,
			resolvedName,
		); err != nil {
			return "", "", err
		}
		reservationName, err := secretKeyReservationName(nameTemplate, path, destinationType, destinationName)
		if err != nil {
			return "", "", err
		}
		return "", reservationName, nil
	default:
		return "", "", fmt.Errorf("unsupported granularity %q", granularity)
	}
}

func resolveSecretPathAssociationName(
	path string,
	destinationType string,
	destinationName string,
	nameTemplate string,
	resolvedName string,
) (string, error) {
	if resolvedName == "" {
		renderedName, err := renderAssociationObjectName(
			nameTemplate,
			path,
			destinationType,
			destinationName,
			syncObjectIDSecretPath,
		)
		if err != nil {
			return "", err
		}
		resolvedName = renderedName
	}
	resolvedName = strings.Trim(resolvedName, "/")
	if resolvedName == "" {
		return "", fmt.Errorf("resolved_name must not be empty")
	}
	return resolvedName, nil
}

func validateSecretKeyAssociationTemplate(
	path string,
	destinationType string,
	destinationName string,
	nameTemplate string,
	resolvedName string,
) error {
	if resolvedName != "" {
		return fmt.Errorf("secret-key granularity requires name_template instead of resolved_name")
	}
	if !strings.Contains(nameTemplate, "{{ key }}") {
		return fmt.Errorf("secret-key name_template must include {{ key }}")
	}
	_, err := renderAssociationObjectName(
		nameTemplate,
		path,
		destinationType,
		destinationName,
		"key",
	)
	return err
}

func associationWithConcreteReservationNames(
	record associationRecord,
	data secretPayload,
) (associationRecord, error) {
	if record.Granularity != syncGranularitySecretKey {
		record.ReservationNames = nil
		return record, nil
	}
	objectIDs, err := associationObjectIDs(record, data)
	if err != nil {
		return associationRecord{}, err
	}
	reservationNames := make([]string, 0, len(objectIDs))
	for _, objectID := range objectIDs {
		resolvedName, err := associationResolvedNameForObject(record, objectID)
		if err != nil {
			return associationRecord{}, err
		}
		reservationNames = append(reservationNames, resolvedName)
	}
	record.ReservationNames = uniqueSortedStrings(reservationNames)
	return record, nil
}

func validateAssociationNameReservations(
	ctx context.Context,
	storage logical.Storage,
	destinationReference string,
	reservationNames []string,
	associationID string,
) error {
	for _, reservationName := range reservationNames {
		reservations, err := listAssociationNameReservationIDs(ctx, storage, destinationReference, reservationName)
		if err != nil {
			return err
		}
		if len(reservations) == 0 || (len(reservations) == 1 && reservations[0] == associationID) {
			continue
		}
		return fmt.Errorf(
			"resolved_name %q is already reserved for destination %s",
			reservationName,
			destinationReference,
		)
	}
	return nil
}

func validateAssociationCapabilities(capabilities providers.Capabilities, granularity string, format string) error {
	if err := validateAssociationFormat(granularity, format); err != nil {
		return err
	}
	switch granularity {
	case syncGranularitySecretPath:
		if !capabilities.SupportsSecretPath {
			return fmt.Errorf("destination provider does not support secret-path granularity")
		}
	case syncGranularitySecretKey:
		if !capabilities.SupportsSecretKey {
			return fmt.Errorf("destination provider does not support secret-key granularity")
		}
	default:
		return fmt.Errorf("unsupported granularity %q", granularity)
	}
	return nil
}

func validateAssociationDataMapping(
	capabilities providers.Capabilities,
	granularity string,
	format string,
	dataMapping string,
	dataKeyTemplate string,
) error {
	switch dataMapping {
	case "", defaultDataMapping:
		if strings.TrimSpace(dataKeyTemplate) != "" {
			return fmt.Errorf("data_key_template requires data_mapping %q", dataMappingSourceKeys)
		}
		return nil
	case dataMappingSourceKeys:
		if !capabilities.SupportsDataMap {
			return fmt.Errorf("destination provider does not support source-key data mapping")
		}
		if granularity != syncGranularitySecretPath {
			return fmt.Errorf("data_mapping %q requires secret-path granularity", dataMappingSourceKeys)
		}
		if format != defaultAssociationFormat {
			return fmt.Errorf("data_mapping %q requires format %q", dataMappingSourceKeys, defaultAssociationFormat)
		}
		if !strings.Contains(dataKeyTemplate, "{{ key }}") {
			return fmt.Errorf("data_key_template must include {{ key }}")
		}
		rendered, err := renderDataKeyTemplate(dataKeyTemplate, "key")
		if err != nil {
			return err
		}
		if err := validateDataMapKey(rendered); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported data_mapping %q", dataMapping)
	}
}

func validateAssociationFormat(granularity string, format string) error {
	switch format {
	case defaultAssociationFormat:
		return nil
	case rawAssociationFormat:
		if granularity != syncGranularitySecretKey {
			return fmt.Errorf("format %q requires secret-key granularity", rawAssociationFormat)
		}
		return nil
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func validateDeleteMode(capabilities providers.Capabilities, deleteMode string) error {
	switch deleteMode {
	case deleteModeRetain, deleteModeOrphan:
		return nil
	case deleteModeDelete:
		if !capabilities.SupportsDeleteIfOwned {
			return fmt.Errorf("delete_mode %q requires provider delete-if-owned capability", deleteMode)
		}
		return nil
	default:
		return fmt.Errorf("unsupported delete_mode %q", deleteMode)
	}
}

func validateAssociationActivation(record associationRecord, metadata *metadataRecord, cfg globalConfig) error {
	if !record.Enabled {
		return nil
	}
	return validateSourceEligibility(metadata, cfg)
}

func validateSourceEligibility(metadata *metadataRecord, cfg globalConfig) error {
	if !sourceSyncRequired(cfg) {
		return nil
	}
	if metadata == nil || !metadata.SourceSyncEnabled {
		return fmt.Errorf(
			"source path is not eligible for sync: source sync must be enabled when security_posture=hardened",
		)
	}
	return nil
}

func stringFromField(data *framework.FieldData, key string, fallback string) string {
	value := strings.TrimSpace(data.Get(key).(string))
	if value == "" {
		return fallback
	}
	return value
}

func boolFromField(data *framework.FieldData, key string, fallback bool) bool {
	value, ok := data.GetOk(key)
	if !ok {
		return fallback
	}
	return value.(bool)
}

func associationResponse(record associationRecord) map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("id", record.ID),
		responseField("path", record.Path),
		responseField("destination", newResponseData(
			responseField("type", record.DestinationType),
			responseField("name", record.DestinationName),
		)),
		responseField("destination_ref", record.DestinationRef),
		responseField("name_template", record.NameTemplate),
		responseField("resolved_name", record.ResolvedName),
		responseField("granularity", record.Granularity),
		responseField("format", record.Format),
		responseField("data_mapping", normalizedDataMapping(record.DataMapping)),
		responseField("data_key_template", record.DataKeyTemplate),
		responseField("provider_config", copyStringMap(record.ProviderConfig)),
		responseField("delete_mode", normalizedDeleteMode(record.DeleteMode)),
		responseField("enabled", record.Enabled),
		responseField("created_time", record.CreatedTime),
		responseField("updated_time", record.UpdatedTime),
	)
}

func associationDefaultsResponse() map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("granularity", syncGranularitySecretPath),
		responseField("format", defaultAssociationFormat),
		responseField("data_mapping", defaultDataMapping),
		responseField("data_key_template", ""),
		responseField("delete_mode", defaultDeleteMode),
		responseField("enabled", true),
		responseField("secret_path_name_template", defaultNameTemplate),
		responseField("secret_key_name_template", defaultPerKeyNameTemplate),
	)
}

func normalizedDeleteMode(deleteMode string) string {
	if deleteMode == "" {
		return defaultDeleteMode
	}
	return deleteMode
}

func normalizedDataMapping(dataMapping string) string {
	if dataMapping == "" {
		return defaultDataMapping
	}
	return dataMapping
}
