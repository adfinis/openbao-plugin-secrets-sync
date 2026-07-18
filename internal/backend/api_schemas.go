package backend

import (
	"net/http"

	"github.com/openbao/openbao/sdk/v2/framework"
)

const apiOperationPrefix = "secret-sync"

func apiPaths(paths []*framework.Path) []*framework.Path {
	for _, path := range paths {
		if path.DisplayAttrs == nil {
			path.DisplayAttrs = &framework.DisplayAttributes{}
		}
		if path.DisplayAttrs.OperationPrefix == "" {
			path.DisplayAttrs.OperationPrefix = apiOperationPrefix
		}
	}
	return paths
}

func apiOKResponse(
	schemaName string,
	description string,
	fields map[string]*framework.FieldSchema,
) map[int][]framework.Response {
	return map[int][]framework.Response{
		http.StatusOK: {
			{
				Description: description,
				Fields:      fields,
				SchemaName:  schemaName,
			},
		},
	}
}

func apiNoContentResponse(description string) map[int][]framework.Response {
	return map[int][]framework.Response{
		http.StatusNoContent: {
			{Description: description},
		},
	}
}

func apiField(fieldType framework.FieldType, description string) *framework.FieldSchema {
	return &framework.FieldSchema{
		Type:        fieldType,
		Description: description,
	}
}

func apiListResponse() map[int][]framework.Response {
	return apiOKResponse("SecretSyncListResponse", "Listed resources.", map[string]*framework.FieldSchema{
		"keys": apiField(framework.TypeStringSlice, "Resource keys in lexical order."),
	})
}

func apiInfoResponse() map[int][]framework.Response {
	return apiOKResponse("SecretSyncInfoResponse", "Static plugin information.", map[string]*framework.FieldSchema{
		"plugin_version": apiField(framework.TypeString, "Running plugin version."),
		"defaults":       apiField(framework.TypeMap, "Static association defaults."),
		"providers":      apiField(framework.TypeMap, "Registered provider capabilities."),
	})
}

//nolint:dupl // Declarative schemas naturally share structure.
func apiConfigResponse() map[int][]framework.Response {
	return apiOKResponse(
		"SecretSyncConfigResponse",
		"Mount-wide secret sync configuration.",
		map[string]*framework.FieldSchema{
			"security_posture": apiField(framework.TypeString, "Mount-wide security posture."),
			"disabled": apiField(
				framework.TypeBool,
				"Whether provider traffic and remote mutation are disabled.",
			),
			"restore_guard": apiField(
				framework.TypeBool,
				"Whether restore guard blocks remote mutation.",
			),
			"restore_guard_acknowledged_time": apiField(framework.TypeString, "Last restore guard acknowledgement time."),
			"restore_epoch":                   apiField(framework.TypeString, "Current restore epoch identifier."),
			"mount_uuid": apiField(
				framework.TypeString,
				"OpenBao-provided UUID for the mounted backend.",
			),
			"storage_schema_version":                apiField(framework.TypeInt, "Current storage schema version."),
			"storage_schema_min_compatible_version": apiField(framework.TypeInt, "Minimum compatible storage schema version."),
			"queue_capacity":                        apiField(framework.TypeInt, "Maximum queued operations."),
			"drift_repair":                          apiField(framework.TypeString, "Background drift policy."),
			"drift_reconcile_interval": apiField(
				framework.TypeString,
				"Minimum interval between background drift checks.",
			),
			"drift_reconcile_batch": apiField(framework.TypeInt, "Maximum objects checked by one drift sweep."),
			"event_dispatch_enabled": apiField(
				framework.TypeBool,
				"Whether enqueue-producing writes wake the dispatcher.",
			),
			"event_dispatch_max_operations": apiField(
				framework.TypeInt,
				"Maximum operations processed by one dispatcher wakeup.",
			),
		})
}

func apiSourceCheckResponse() map[int][]framework.Response {
	return apiOKResponse("SecretSyncSourceCheckResponse", "Source sync readiness.", map[string]*framework.FieldSchema{
		"path":                      apiField(framework.TypeString, "Source secret path."),
		"ready":                     apiField(framework.TypeBool, "Whether the source is ready for sync."),
		"source_sync_enabled":       apiField(framework.TypeBool, "Whether source sync is enabled."),
		"source_sync_required":      apiField(framework.TypeBool, "Whether the active posture requires source enablement."),
		"current_version":           apiField(framework.TypeInt, "Current source version."),
		"current_version_available": apiField(framework.TypeBool, "Whether the current source version can be read."),
		"association_count":         apiField(framework.TypeInt, "Number of configured associations."),
		"enabled_association_count": apiField(framework.TypeInt, "Number of enabled associations."),
		"queued_operations":         apiField(framework.TypeInt, "Number of queued operations for the source."),
		"status_objects":            apiField(framework.TypeInt, "Number of status objects for the source."),
		"blockers":                  apiField(framework.TypeStringSlice, "Readiness blocker identifiers."),
	})
}

func apiSourceSyncResponse() map[int][]framework.Response {
	return apiOKResponse("SecretSyncSourceSyncResponse", "Updated source sync state.", map[string]*framework.FieldSchema{
		"path":                apiField(framework.TypeString, "Source secret path."),
		"source_sync_enabled": apiField(framework.TypeBool, "Whether source sync is enabled."),
		"changed":             apiField(framework.TypeBool, "Whether the stored state changed."),
		"sync_operation_ids":  apiField(framework.TypeStringSlice, "Operations enqueued by enabling sync."),
		"sync_state":          apiField(framework.TypeString, "Aggregate sync state."),
		"metadata":            apiField(framework.TypeMap, "Current source metadata."),
	})
}

func apiMetadataResponse() map[int][]framework.Response {
	return apiOKResponse(
		"SecretSyncMetadataResponse",
		"Source metadata and sync summary.",
		map[string]*framework.FieldSchema{
			"current_version":      apiField(framework.TypeInt, "Current source version."),
			"oldest_version":       apiField(framework.TypeInt, "Oldest retained source version."),
			"max_versions":         apiField(framework.TypeInt, "Maximum retained source versions."),
			"cas_required":         apiField(framework.TypeBool, "Whether writes require check-and-set."),
			"delete_version_after": apiField(framework.TypeString, "Stored version deletion interval."),
			"custom_metadata":      apiField(framework.TypeMap, "Non-secret custom metadata."),
			"source_sync_enabled":  apiField(framework.TypeBool, "Whether source sync is enabled."),
			"updated_time":         apiField(framework.TypeString, "Metadata update time."),
			"versions":             apiField(framework.TypeMap, "Per-version metadata."),
			"sync":                 apiField(framework.TypeMap, "Queue and status summary."),
		})
}

func apiSourceDataReadResponse() map[int][]framework.Response {
	return apiOKResponse(
		"SecretSyncSourceDataReadResponse",
		"Source secret data and version metadata.",
		map[string]*framework.FieldSchema{
			"data":     apiField(framework.TypeMap, "Source secret payload."),
			"metadata": apiField(framework.TypeMap, "Current version metadata."),
		})
}

func apiSourceDataMutationResponse() map[int][]framework.Response {
	return apiOKResponse(
		"SecretSyncSourceDataMutationResponse",
		"Source version mutation metadata.",
		map[string]*framework.FieldSchema{
			"metadata": apiField(framework.TypeMap, "Created or deleted version metadata."),
		})
}

func apiDestinationResponse() map[int][]framework.Response {
	return apiOKResponse(
		"SecretSyncDestinationResponse",
		"Redacted destination configuration.",
		map[string]*framework.FieldSchema{
			"type":                           apiField(framework.TypeString, "Destination provider type."),
			"name":                           apiField(framework.TypeString, "Destination name."),
			"description":                    apiField(framework.TypeString, "Destination description."),
			"disabled":                       apiField(framework.TypeBool, "Whether the destination is disabled."),
			"allowed_source_path_prefixes":   apiField(framework.TypeStringSlice, "Allowed source path prefixes."),
			"allowed_resolved_name_prefixes": apiField(framework.TypeStringSlice, "Allowed resolved remote name prefixes."),
			"config":                         apiField(framework.TypeMap, "Non-sensitive provider configuration."),
			"sensitive_config":               apiField(framework.TypeMap, "Redacted sensitive configuration summary."),
			"capabilities":                   apiField(framework.TypeMap, "Provider capabilities."),
			"created_time":                   apiField(framework.TypeString, "Creation time."),
			"updated_time":                   apiField(framework.TypeString, "Last update time."),
		})
}

func apiDestinationCheckResponse() map[int][]framework.Response {
	return apiOKResponse("SecretSyncDestinationCheckResponse", "Destination readiness.", map[string]*framework.FieldSchema{
		"ready":                  apiField(framework.TypeBool, "Whether the destination is ready."),
		"valid":                  apiField(framework.TypeBool, "Whether provider configuration is valid."),
		"healthy":                apiField(framework.TypeBool, "Whether the provider health check succeeded."),
		"health_checked":         apiField(framework.TypeBool, "Whether provider health was checked."),
		"disabled":               apiField(framework.TypeBool, "Whether the destination is disabled."),
		"destination":            apiField(framework.TypeMap, "Destination identity."),
		"capabilities":           apiField(framework.TypeMap, "Provider capabilities."),
		"blockers":               apiField(framework.TypeStringSlice, "Readiness blocker identifiers."),
		"validation_error_class": apiField(framework.TypeString, "Provider validation error class."),
		"validation_message":     apiField(framework.TypeString, "Provider validation message."),
		"health_error_class":     apiField(framework.TypeString, "Provider health error class."),
		"health_message":         apiField(framework.TypeString, "Provider health message."),
	})
}

func apiDestinationValidationResponse() map[int][]framework.Response {
	return apiOKResponse(
		"SecretSyncDestinationValidationResponse",
		"Destination validation result.",
		map[string]*framework.FieldSchema{
			"valid":        apiField(framework.TypeBool, "Whether provider configuration is valid."),
			"destination":  apiField(framework.TypeMap, "Destination identity."),
			"capabilities": apiField(framework.TypeMap, "Provider capabilities."),
			"error_class":  apiField(framework.TypeString, "Provider error class."),
			"error":        apiField(framework.TypeString, "Provider validation error."),
		})
}

func apiDestinationHealthResponse() map[int][]framework.Response {
	return apiOKResponse(
		"SecretSyncDestinationHealthResponse",
		"Destination health result.",
		map[string]*framework.FieldSchema{
			"healthy":     apiField(framework.TypeBool, "Whether provider health checks succeeded."),
			"destination": apiField(framework.TypeMap, "Destination identity."),
			"disabled":    apiField(framework.TypeBool, "Whether the destination is disabled."),
			"error_class": apiField(framework.TypeString, "Provider error class."),
			"message":     apiField(framework.TypeString, "Provider health message."),
		})
}

//nolint:dupl // Declarative schemas naturally share structure.
func apiAssociationLifecycleResponse() map[int][]framework.Response {
	return apiOKResponse(
		"SecretSyncAssociationLifecycleResponse",
		"Association lifecycle result.",
		map[string]*framework.FieldSchema{
			"association_id":         apiField(framework.TypeString, "Association identifier."),
			"destination_ref":        apiField(framework.TypeString, "Destination reference."),
			"resolved_name":          apiField(framework.TypeString, "Resolved remote object name."),
			"granularity":            apiField(framework.TypeString, "Sync granularity."),
			"format":                 apiField(framework.TypeString, "Payload format."),
			"data_mapping":           apiField(framework.TypeString, "Data mapping mode."),
			"data_key_template":      apiField(framework.TypeString, "Per-key payload key template."),
			"provider_config":        apiField(framework.TypeMap, "Provider-specific association configuration."),
			"delete_mode":            apiField(framework.TypeString, "Remote delete policy."),
			"enabled":                apiField(framework.TypeBool, "Whether the association is enabled."),
			"sync_operation_ids":     apiField(framework.TypeStringSlice, "Enqueued sync operation identifiers."),
			"canceled_operation_ids": apiField(framework.TypeStringSlice, "Canceled operation identifiers."),
			"hint":                   apiField(framework.TypeString, "Operator-facing diagnostic hint."),
			"next_actions":           apiField(framework.TypeSlice, "Structured operator actions."),
		})
}

func apiAssociationsResponse() map[int][]framework.Response {
	return apiOKResponse(
		"SecretSyncAssociationsResponse",
		"Associations for a source path.",
		map[string]*framework.FieldSchema{
			"path":         apiField(framework.TypeString, "Source secret path."),
			"associations": apiField(framework.TypeSlice, "Configured associations."),
		})
}

func apiAssociationResponse() map[int][]framework.Response {
	return apiOKResponse("SecretSyncAssociationResponse", "Association configuration.", map[string]*framework.FieldSchema{
		"id":                apiField(framework.TypeString, "Association identifier."),
		"path":              apiField(framework.TypeString, "Source secret path."),
		"destination":       apiField(framework.TypeMap, "Destination identity."),
		"destination_ref":   apiField(framework.TypeString, "Destination reference."),
		"name_template":     apiField(framework.TypeString, "Destination name template."),
		"resolved_name":     apiField(framework.TypeString, "Resolved remote object name."),
		"granularity":       apiField(framework.TypeString, "Sync granularity."),
		"format":            apiField(framework.TypeString, "Payload format."),
		"data_mapping":      apiField(framework.TypeString, "Data mapping mode."),
		"data_key_template": apiField(framework.TypeString, "Per-key payload key template."),
		"provider_config":   apiField(framework.TypeMap, "Provider-specific association configuration."),
		"delete_mode":       apiField(framework.TypeString, "Remote delete policy."),
		"enabled":           apiField(framework.TypeBool, "Whether the association is enabled."),
		"created_time":      apiField(framework.TypeString, "Creation time."),
		"updated_time":      apiField(framework.TypeString, "Last update time."),
	})
}

func apiAssociationPlanResponse() map[int][]framework.Response {
	return apiOKResponse(
		"SecretSyncAssociationPlanResponse",
		"Non-mutating association plan.",
		map[string]*framework.FieldSchema{
			"path":              apiField(framework.TypeString, "Source secret path."),
			"version":           apiField(framework.TypeInt, "Current source version."),
			"action":            apiField(framework.TypeString, "Planned provider action."),
			"source_eligible":   apiField(framework.TypeBool, "Whether the source is eligible for sync."),
			"association_id":    apiField(framework.TypeString, "Association identifier."),
			"destination_ref":   apiField(framework.TypeString, "Destination reference."),
			"association":       apiField(framework.TypeMap, "Effective association."),
			"destination":       apiField(framework.TypeMap, "Destination identity."),
			"resolved_name":     apiField(framework.TypeString, "Resolved remote object name."),
			"granularity":       apiField(framework.TypeString, "Sync granularity."),
			"format":            apiField(framework.TypeString, "Payload format."),
			"data_mapping":      apiField(framework.TypeString, "Data mapping mode."),
			"data_key_template": apiField(framework.TypeString, "Per-key payload key template."),
			"payload_bytes":     apiField(framework.TypeInt, "Serialized payload size."),
			"error_class":       apiField(framework.TypeString, "Provider error class."),
			"message":           apiField(framework.TypeString, "Provider plan message."),
			"objects":           apiField(framework.TypeSlice, "Per-object plan results."),
		})
}

func apiQueueSummaryResponse() map[int][]framework.Response {
	return apiOKResponse("SecretSyncQueueSummaryResponse", "Durable queue summary.", map[string]*framework.FieldSchema{
		"pending":            apiField(framework.TypeInt, "Pending operation count."),
		"retry_wait":         apiField(framework.TypeInt, "Retry-wait operation count."),
		"claimed":            apiField(framework.TypeInt, "Actively claimed operation count."),
		"terminal":           apiField(framework.TypeInt, "Terminal operation count."),
		"oldest_age_seconds": apiField(framework.TypeInt, "Age of the oldest queued operation."),
		"capacity":           apiField(framework.TypeInt, "Configured queue capacity."),
		"utilization":        apiField(framework.TypeFloat, "Queue utilization ratio."),
	})
}

func apiQueueDrainResponse() map[int][]framework.Response {
	return apiOKResponse("SecretSyncQueueDrainResponse", "Queue drain result.", map[string]*framework.FieldSchema{
		"processed":      apiField(framework.TypeInt, "Processed operation count."),
		"max_operations": apiField(framework.TypeInt, "Requested processing bound."),
		"queue":          apiField(framework.TypeMap, "Queue summary after processing."),
	})
}

func apiQueueOperationResponse() map[int][]framework.Response {
	return apiOKResponse("SecretSyncQueueOperationResponse", "Durable queue operation.", map[string]*framework.FieldSchema{
		"id":                 apiField(framework.TypeString, "Operation identifier."),
		"type":               apiField(framework.TypeString, "Operation type."),
		"path":               apiField(framework.TypeString, "Source secret path."),
		"version":            apiField(framework.TypeInt, "Source version."),
		"association_id":     apiField(framework.TypeString, "Association identifier."),
		"object_id":          apiField(framework.TypeString, "Provider object identifier."),
		"destination_ref":    apiField(framework.TypeString, "Destination reference."),
		"state":              apiField(framework.TypeString, "Queue state."),
		"trigger":            apiField(framework.TypeString, "Operation trigger."),
		"attempts":           apiField(framework.TypeInt, "Completed attempt count."),
		"not_before":         apiField(framework.TypeString, "Earliest retry time."),
		"created_time":       apiField(framework.TypeString, "Creation time."),
		"updated_time":       apiField(framework.TypeString, "Last update time."),
		"idempotency_key":    apiField(framework.TypeString, "Idempotency key."),
		"claimed":            apiField(framework.TypeBool, "Whether the operation has an active claim."),
		"claim_owner":        apiField(framework.TypeString, "Active claim owner."),
		"claim_expires_time": apiField(framework.TypeString, "Active claim expiry time."),
		"claim_attempt":      apiField(framework.TypeInt, "Attempt protected by the active claim."),
	})
}

func apiStatusResponse() map[int][]framework.Response {
	return apiOKResponse("SecretSyncStatusResponse", "Per-source sync status.", map[string]*framework.FieldSchema{
		"path":          apiField(framework.TypeString, "Source secret path."),
		"version":       apiField(framework.TypeInt, "Current source version."),
		"state":         apiField(framework.TypeString, "Aggregate sync state."),
		"operation_ids": apiField(framework.TypeStringSlice, "Queued operation identifiers."),
		"objects":       apiField(framework.TypeSlice, "Per-object sync status."),
	})
}

func apiReconcileResponse() map[int][]framework.Response {
	return apiOKResponse("SecretSyncReconcileResponse", "Reconcile result.", map[string]*framework.FieldSchema{
		"path":    apiField(framework.TypeString, "Source secret path."),
		"version": apiField(framework.TypeInt, "Current source version."),
		"applied": apiField(framework.TypeBool, "Whether local status was updated."),
		"state":   apiField(framework.TypeString, "Aggregate sync state."),
		"objects": apiField(framework.TypeSlice, "Per-object reconcile results."),
	})
}
