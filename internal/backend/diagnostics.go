package backend

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const mountPathPlaceholder = "<mount>"

type diagnostic struct {
	Hint        string
	NextActions []diagnosticAction
}

type diagnosticAction struct {
	Action        string
	Operation     string
	Path          string
	Mount         string
	Parameters    map[string]string
	Force         bool
	MutatesRemote bool
}

func requestMountPath(req *logical.Request) string {
	if req == nil {
		return mountPathPlaceholder
	}
	mount := strings.Trim(req.MountPoint, "/")
	if mount == "" {
		return mountPathPlaceholder
	}
	return mount
}

func diagnosticResponseFields(diagnostic diagnostic) []responseEntry {
	fields := make([]responseEntry, 0, 2)
	if diagnostic.Hint != "" {
		fields = append(fields, responseField("hint", diagnostic.Hint))
	}
	if len(diagnostic.NextActions) > 0 {
		fields = append(fields, responseField("next_actions", diagnosticActionsResponse(diagnostic.NextActions)))
	}
	return fields
}

func errorResponseWithDiagnostic(message string, diagnostic diagnostic) *logical.Response {
	response := logical.ErrorResponse(message)
	fields := diagnosticResponseFields(diagnostic)
	if len(fields) > 0 {
		response.Data["data"] = newResponseData(fields...)
	}
	return response
}

func errorResponseForOperationError(err error, mount string) *logical.Response {
	if errors.Is(err, errQueueCapacity) {
		return errorResponseWithDiagnostic(err.Error(), queueCapacityDiagnostic(mount))
	}
	return logical.ErrorResponse(err.Error())
}

func diagnosticActionsResponse(actions []diagnosticAction) []map[string]interface{} { //nolint:forbidigo
	response := make([]map[string]interface{}, 0, len(actions))
	for _, action := range actions {
		response = append(response, diagnosticActionResponse(action))
	}
	return response
}

func diagnosticActionResponse(action diagnosticAction) map[string]interface{} { //nolint:forbidigo
	fields := []responseEntry{
		responseField("action", action.Action),
		responseField("operation", action.Operation),
		responseField("path", action.Path),
		responseField("mutates_remote", action.MutatesRemote),
		responseField("command", diagnosticCommand(action)),
	}
	if len(action.Parameters) > 0 {
		fields = append(fields, responseField("parameters", action.Parameters))
	}
	if action.Force {
		fields = append(fields, responseField("force", true))
	}
	return newResponseData(fields...)
}

func associationAlreadyEnabledDiagnostic(mount string, record associationRecord) diagnostic {
	return diagnostic{
		Hint: "Association is already enabled, so this update did not enqueue sync work. " +
			"Run manual sync to push or retry the current source version.",
		NextActions: []diagnosticAction{
			manualAssociationSyncAction(mount, record.Path, record.DestinationRef),
		},
	}
}

func associationDisabledDiagnostic(mount string, record associationRecord) diagnostic {
	return diagnostic{
		Hint: "Association is disabled. Enable it before enqueueing manual sync work.",
		NextActions: []diagnosticAction{
			enableAssociationAction(mount, record.Path, record.DestinationRef),
		},
	}
}

func validationDiagnosticForAssociation(mount string, record associationRecord, message string) diagnostic {
	return validationDiagnostic(mount, record.Path, record.DestinationRef, message)
}

func validationDiagnostic(mount, sourcePath, destinationRef, message string) diagnostic {
	normalized := strings.ToLower(message)
	switch {
	case strings.Contains(normalized, "source path is not eligible") ||
		strings.Contains(normalized, "custom_metadata.syncable"):
		return diagnostic{
			Hint: "Source opt-in is required. Mark the source path syncable before enabling or syncing this association.",
			NextActions: []diagnosticAction{
				enableSourceAction(mount, sourcePath),
			},
		}
	case strings.Contains(normalized, destinationUnconstrainedBlocker):
		return diagnostic{
			Hint: "Delegated mode requires destination source-path and resolved-name constraints " +
				"before this association can sync.",
			NextActions: []diagnosticAction{
				readDestinationAction(mount, destinationRef),
			},
		}
	case strings.Contains(normalized, "destination") && strings.Contains(normalized, "disabled"):
		return diagnostic{
			Hint: "Destination is disabled. Enable the destination before syncing this association.",
			NextActions: []diagnosticAction{
				enableDestinationAction(mount, destinationRef),
			},
		}
	default:
		return diagnostic{
			Hint: "Validation failed. Inspect the association plan, fix the reported validation issue, then retry manual sync.",
			NextActions: []diagnosticAction{
				associationPlanAction(mount, sourcePath, destinationRef),
			},
		}
	}
}

func restoreGuardDiagnostic(mount string) diagnostic {
	return diagnostic{
		Hint: "Restore guard is active. Acknowledge the restore guard after reviewing destination state " +
			"before draining queued remote mutations.",
		NextActions: []diagnosticAction{
			{
				Action:    "acknowledge_restore_guard",
				Operation: "write",
				Path:      "config/restore-guard/acknowledge",
				Mount:     mount,
				Force:     true,
			},
		},
	}
}

func mountDisabledDiagnostic(mount string) diagnostic {
	return diagnostic{
		Hint: "Secret Sync is disabled. Set config disabled=false before draining queued remote mutations " +
			"or relying on background dispatch.",
		NextActions: []diagnosticAction{
			{
				Action:    "enable_secret_sync",
				Operation: "write",
				Path:      "config",
				Mount:     mount,
				Parameters: map[string]string{
					"disabled": "false",
				},
			},
		},
	}
}

func remoteMutationUnsafeDiagnostic() diagnostic {
	return diagnostic{
		Hint: "Remote mutation is not allowed on this replication node. Run remote mutation commands on " +
			"an active node that owns provider writes.",
	}
}

func queueCapacityDiagnostic(mount string) diagnostic {
	return diagnostic{
		Hint: "Queue capacity is exhausted. Drain, retry, or cancel queued work, or raise config queue_capacity " +
			"before retrying this operation.",
		NextActions: []diagnosticAction{
			readQueueAction(mount),
			drainQueueAction(mount),
			readConfigAction(mount),
		},
	}
}

func statusDiagnosticForRecord(mount string, record statusRecord) diagnostic {
	return syncStateDiagnostic(
		mount,
		domain.SyncState(record.State),
		record.Path,
		record.DestinationRef,
		record.LastOperationID,
		record.LastError,
	)
}

func reconcileDiagnosticForResult(mount string, result reconcileObjectResult) diagnostic {
	return syncStateDiagnostic(
		mount,
		result.state,
		result.association.Path,
		result.association.DestinationRef,
		"",
		result.message,
	)
}

func syncStateDiagnostic(
	mount string,
	state domain.SyncState,
	sourcePath string,
	destinationRef string,
	operationID string,
	message string,
) diagnostic {
	switch state {
	case domain.SyncStateRemoteOwnershipLost:
		return diagnostic{
			Hint: "Remote object ownership does not match this association. Inspect or remove the remote " +
				"object first, then run manual sync to let OpenBao recreate it.",
			NextActions: []diagnosticAction{
				manualAssociationSyncAction(mount, sourcePath, destinationRef),
			},
		}
	case domain.SyncStateRemoteMissing:
		return diagnostic{
			Hint: "Remote object is missing. Run manual sync to recreate it from the current OpenBao source version.",
			NextActions: []diagnosticAction{
				manualAssociationSyncAction(mount, sourcePath, destinationRef),
			},
		}
	case domain.SyncStateDrifted:
		return diagnostic{
			Hint: "Remote object differs from the current OpenBao source version. Run manual sync to restore " +
				"OpenBao as the source of truth.",
			NextActions: []diagnosticAction{
				manualAssociationSyncAction(mount, sourcePath, destinationRef),
			},
		}
	case domain.SyncStateValidationError:
		return validationDiagnostic(mount, sourcePath, destinationRef, message)
	case domain.SyncStateQueueBlocked:
		diagnostic := diagnostic{
			Hint: "Sync work is blocked by queue or provider capacity. Inspect queue/config state and retry " +
				"the operation after capacity is available.",
			NextActions: []diagnosticAction{
				readQueueAction(mount),
				readConfigAction(mount),
			},
		}
		if operationID != "" {
			diagnostic.NextActions = append(diagnostic.NextActions, retryOperationAction(mount, operationID))
		}
		return diagnostic
	case domain.SyncStateDisabled:
		return diagnostic{
			Hint: "Association or destination is disabled. Enable the disabled object before retrying sync.",
			NextActions: []diagnosticAction{
				enableAssociationAction(mount, sourcePath, destinationRef),
				readDestinationAction(mount, destinationRef),
			},
		}
	case domain.SyncStateDestinationAuthError:
		return destinationFailureDiagnostic(
			mount,
			destinationRef,
			operationID,
			"Destination authentication failed. Check destination credentials, then retry queued work.",
		)
	case domain.SyncStateDestinationPolicyError:
		return destinationFailureDiagnostic(
			mount,
			destinationRef,
			operationID,
			"Destination authorization failed. Check provider permissions for the configured destination, "+
				"then retry queued work.",
		)
	case domain.SyncStateDestinationRateLimited:
		return destinationFailureDiagnostic(
			mount,
			destinationRef,
			operationID,
			"Destination rate limited the request. Let retry-wait work become due or retry after provider limits recover.",
		)
	case domain.SyncStateDestinationUnavailable:
		return destinationFailureDiagnostic(
			mount,
			destinationRef,
			operationID,
			"Destination is unavailable. Check destination health, then retry queued work after recovery.",
		)
	case domain.SyncStateInternalError:
		if operationID == "" {
			return diagnostic{
				Hint: "Internal sync error recorded. Inspect plugin logs and retry after the underlying issue is fixed.",
			}
		}
		return diagnostic{
			Hint: "Internal sync error recorded. Inspect plugin logs and retry queued work after the underlying issue is fixed.",
			NextActions: []diagnosticAction{
				retryOperationAction(mount, operationID),
			},
		}
	default:
		return diagnostic{}
	}
}

func destinationFailureDiagnostic(mount, destinationRef, operationID, hint string) diagnostic {
	diagnostic := diagnostic{
		Hint: hint,
		NextActions: []diagnosticAction{
			destinationHealthAction(mount, destinationRef),
		},
	}
	if operationID != "" {
		diagnostic.NextActions = append(diagnostic.NextActions, retryOperationAction(mount, operationID))
	}
	return diagnostic
}

func manualAssociationSyncAction(mount, sourcePath, destinationRef string) diagnosticAction {
	return diagnosticAction{
		Action:        "manual_sync",
		Operation:     "write",
		Path:          fmt.Sprintf("associations/%s/sync", sourcePath),
		Mount:         mount,
		Parameters:    destinationParameter(destinationRef),
		MutatesRemote: true,
	}
}

func enableAssociationAction(mount, sourcePath, destinationRef string) diagnosticAction {
	return diagnosticAction{
		Action:     "enable_association",
		Operation:  "write",
		Path:       fmt.Sprintf("associations/%s/enable", sourcePath),
		Mount:      mount,
		Parameters: destinationParameter(destinationRef),
	}
}

func associationPlanAction(mount, sourcePath, destinationRef string) diagnosticAction {
	return diagnosticAction{
		Action:     "inspect_association_plan",
		Operation:  "read",
		Path:       fmt.Sprintf("associations/%s/plan", sourcePath),
		Mount:      mount,
		Parameters: destinationParameter(destinationRef),
	}
}

func enableSourceAction(mount, sourcePath string) diagnosticAction {
	return diagnosticAction{
		Action:    "enable_source",
		Operation: "write",
		Path:      fmt.Sprintf("sources/%s/enable", sourcePath),
		Mount:     mount,
	}
}

func enableDestinationAction(mount, destinationRef string) diagnosticAction {
	return diagnosticAction{
		Action:    "enable_destination",
		Operation: "write",
		Path:      fmt.Sprintf("destinations/%s", destinationRef),
		Mount:     mount,
		Parameters: map[string]string{
			"disabled": "false",
		},
	}
}

func readDestinationAction(mount, destinationRef string) diagnosticAction {
	return diagnosticAction{
		Action:    "read_destination",
		Operation: "read",
		Path:      fmt.Sprintf("destinations/%s", destinationRef),
		Mount:     mount,
	}
}

func destinationHealthAction(mount, destinationRef string) diagnosticAction {
	return diagnosticAction{
		Action:    "check_destination_health",
		Operation: "write",
		Path:      fmt.Sprintf("destinations/%s/health", destinationRef),
		Mount:     mount,
	}
}

func readQueueAction(mount string) diagnosticAction {
	return diagnosticAction{
		Action:    "read_queue",
		Operation: "read",
		Path:      "queue",
		Mount:     mount,
	}
}

func drainQueueAction(mount string) diagnosticAction {
	return diagnosticAction{
		Action:        "drain_queue",
		Operation:     "write",
		Path:          "queue/drain",
		Mount:         mount,
		MutatesRemote: true,
	}
}

func readConfigAction(mount string) diagnosticAction {
	return diagnosticAction{
		Action:    "read_config",
		Operation: "read",
		Path:      "config",
		Mount:     mount,
	}
}

func retryOperationAction(mount, operationID string) diagnosticAction {
	return diagnosticAction{
		Action:        "retry_operation",
		Operation:     "write",
		Path:          fmt.Sprintf("queue/%s/retry", operationID),
		Mount:         mount,
		MutatesRemote: true,
	}
}

func destinationParameter(destinationRef string) map[string]string {
	if destinationRef == "" {
		return nil
	}
	return map[string]string{"destination": destinationRef}
}

func diagnosticCommand(action diagnosticAction) string {
	var builder strings.Builder
	builder.WriteString("bao ")
	builder.WriteString(action.Operation)
	builder.WriteString(" ")
	if action.Force {
		builder.WriteString("-force ")
	}
	builder.WriteString(mountedPath(action.Mount, action.Path))
	for _, key := range sortedMapKeys(action.Parameters) {
		builder.WriteString(" ")
		builder.WriteString(key)
		builder.WriteString("=")
		builder.WriteString(action.Parameters[key])
	}
	return builder.String()
}

func mountedPath(mount string, path string) string {
	trimmedMount := strings.Trim(mount, "/")
	if trimmedMount == "" {
		trimmedMount = mountPathPlaceholder
	}
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return trimmedMount
	}
	return trimmedMount + "/" + trimmed
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
