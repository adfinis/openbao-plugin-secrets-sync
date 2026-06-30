package backend

import (
	"context"

	"github.com/adfinis/openbao-secret-sync/internal/domain"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathStatus(_ *secretSyncBackend) *framework.Path {
	return &framework.Path{
		Pattern: "status/" + framework.MatchAllRegex("path"),
		Fields: map[string]*framework.FieldSchema{
			"path": {
				Type:        framework.TypeString,
				Description: "Source secret path.",
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation: &framework.PathOperation{
				Callback: pathStatusRead,
				Summary:  "Read per-path sync status.",
			},
		},
		HelpSynopsis:    "Inspect source path sync status.",
		HelpDescription: "Returns current source version and pending sync operation identifiers.",
	}
}

func pathStatusRead(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	metadata, err := getMetadata(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		return &logical.Response{Data: newResponseData(
			responseField("path", path),
			responseField("state", string(domain.SyncStateUnknown)),
			responseField("operation_ids", []string{}),
			responseField("objects", []string{}),
		)}, nil
	}

	operationIDs, err := listQueuedOutboxIDsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	statusRecords, err := listStatusRecordsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}

	state := domain.SyncStateNoAssociation
	if len(operationIDs) > 0 {
		state = domain.SyncStatePending
	} else if len(statusRecords) > 0 {
		state = domain.SyncStateSynced
		for _, record := range statusRecords {
			if record.State != string(domain.SyncStateSynced) {
				state = domain.SyncState(record.State)
				break
			}
		}
	}
	objects := []map[string]interface{}{} //nolint:forbidigo // OpenBao response boundary.
	for _, record := range statusRecords {
		objects = append(objects, statusResponseObject(record))
	}
	summaryFields := statusSummaryFields(statusRecords)
	fields := make([]responseEntry, 0, 5+len(summaryFields))
	fields = append(fields,
		responseField("path", path),
		responseField("version", metadata.CurrentVersion),
		responseField("state", string(state)),
		responseField("operation_ids", operationIDs),
		responseField("objects", objects),
	)
	fields = append(fields, summaryFields...)
	return &logical.Response{Data: newResponseData(fields...)}, nil
}

func statusResponseObject(record statusRecord) map[string]interface{} { //nolint:forbidigo // OpenBao response boundary.
	return newResponseData(
		responseField("association_id", record.AssociationID),
		responseField("object_id", record.ObjectID),
		responseField("destination_ref", record.DestinationRef),
		responseField("resolved_name", record.ResolvedName),
		responseField("state", record.State),
		responseField("version", record.Version),
		responseField("payload_sha256", record.PayloadSHA256),
		responseField("remote_version", record.RemoteVersion),
		responseField("last_operation_id", record.LastOperationID),
		responseField("last_success_time", record.LastSuccessTime),
		responseField("last_error_class", record.LastErrorClass),
		responseField("last_error", record.LastError),
	)
}

func statusSummaryFields(statusRecords []statusRecord) []responseEntry {
	if len(statusRecords) != 1 {
		return nil
	}
	record := statusRecords[0]
	return []responseEntry{
		responseField("association_id", record.AssociationID),
		responseField("object_id", record.ObjectID),
		responseField("destination_ref", record.DestinationRef),
		responseField("resolved_name", record.ResolvedName),
		responseField("payload_sha256", record.PayloadSHA256),
		responseField("remote_version", record.RemoteVersion),
		responseField("last_operation_id", record.LastOperationID),
		responseField("last_success_time", record.LastSuccessTime),
		responseField("last_error_class", record.LastErrorClass),
		responseField("last_error", record.LastError),
	}
}
