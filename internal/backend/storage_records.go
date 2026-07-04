package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
)

const (
	storageSchemaKey           = "schema/version"
	pluginInstanceKey          = "identity/plugin-instance"
	restoreEpochKey            = "identity/restore-epoch"
	metadataStoragePrefix      = "metadata/"
	versionStoragePrefix       = "data/"
	enqueueIntentStoragePrefix = "enqueue_intent/"
	destinationStoragePrefix   = "destinations/"
	associationStoragePrefix   = "associations/"
	associationByDestPrefix    = "associations_by_destination/"
	associationNamePrefix      = "association_names/"
	outboxStoragePrefix        = "outbox/"
	outboxByDueStoragePrefix   = "outbox_by_due/"
	outboxByPathStoragePrefix  = "outbox_by_path/"
	outboxByStateStoragePrefix = "outbox_by_state/"
	defaultQueueCapacity       = 1000
	defaultDriftRepair         = driftRepairOff
	defaultDriftInterval       = "1h"
	minDriftInterval           = "1m"
	defaultDriftBatch          = 16
	defaultMaxVersions         = 10
	defaultDeleteVersionAfter  = "0s"
	outboxStatePending         = "pending"
	outboxStateRetryWait       = "retry_wait"
	outboxStateFailedTerminal  = "failed_terminal"
	outboxStateCanceled        = "canceled"
	syncGranularitySecretPath  = "secret-path"
	syncGranularitySecretKey   = "secret-key"
	syncObjectIDSecretPath     = syncGranularitySecretPath
	statusStoragePrefix        = "status/"
	providerTypeFake           = "fake"
	defaultAssociationFormat   = "json"
	rawAssociationFormat       = "raw"
	defaultDataMapping         = "payload"
	dataMappingSourceKeys      = "source-keys"
	defaultDataKeyTemplate     = "{{ key }}"
	defaultNameTemplate        = "{{ path }}"
	defaultPerKeyNameTemplate  = "{{ path }}/{{ key }}"
	defaultDeleteMode          = deleteModeRetain
	deleteModeRetain           = "retain"
	deleteModeDelete           = "delete"
	deleteModeOrphan           = "orphan"
	driftRepairOff             = "off"
	driftRepairDetect          = "detect"
	driftRepairRepair          = "repair"
	outboxTriggerUser          = "user"
	outboxTriggerDriftRepair   = "drift-repair"
	sourceMetadataKeySyncable  = "syncable"
	sourceMetadataValueTrue    = "true"
	currentStorageSchema       = 1
	minSupportedStorageSchema  = 1
)

type secretPayload map[string]interface{} //nolint:forbidigo // OpenBao SDK TypeMap uses map[string]interface{}.

type storageSchemaRecord struct {
	Version              int    `json:"version"`
	MinCompatibleVersion int    `json:"min_compatible_version"`
	CreatedTime          string `json:"created_time"`
	UpdatedTime          string `json:"updated_time"`
}

type pluginInstanceRecord struct {
	ID          string `json:"id"`
	CreatedTime string `json:"created_time"`
}

type restoreEpochRecord struct {
	Epoch       string `json:"epoch"`
	CreatedTime string `json:"created_time"`
	UpdatedTime string `json:"updated_time"`
}

type versionRecord struct {
	Version      int           `json:"version"`
	CreatedTime  string        `json:"created_time"`
	Data         secretPayload `json:"data"`
	DeletionTime string        `json:"deletion_time"`
	Destroyed    bool          `json:"destroyed"`
}

type versionMetadata struct {
	CreatedTime  string `json:"created_time"`
	DeletionTime string `json:"deletion_time"`
	Destroyed    bool   `json:"destroyed"`
}

type metadataRecord struct {
	Generation         string                     `json:"generation"`
	CurrentVersion     int                        `json:"current_version"`
	OldestVersion      int                        `json:"oldest_version"`
	MaxVersions        int                        `json:"max_versions"`
	CASRequired        bool                       `json:"cas_required"`
	DeleteVersionAfter string                     `json:"delete_version_after"`
	CustomMetadata     map[string]string          `json:"custom_metadata"`
	Versions           map[string]versionMetadata `json:"versions"`
	UpdatedTime        string                     `json:"updated_time"`
}

type destinationRecord struct {
	Type                        string            `json:"type"`
	Name                        string            `json:"name"`
	Description                 string            `json:"description"`
	Disabled                    bool              `json:"disabled"`
	Config                      map[string]string `json:"config"`
	AllowedSourcePathPrefixes   []string          `json:"allowed_source_path_prefixes,omitempty"`
	AllowedResolvedNamePrefixes []string          `json:"allowed_resolved_name_prefixes,omitempty"`
	CreatedTime                 string            `json:"created_time"`
	UpdatedTime                 string            `json:"updated_time"`
}

type destinationSensitiveRecord struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Config      map[string]string `json:"config"`
	CreatedTime string            `json:"created_time"`
	UpdatedTime string            `json:"updated_time"`
}

type associationRecord struct {
	ID               string   `json:"id"`
	Path             string   `json:"path"`
	DestinationType  string   `json:"destination_type"`
	DestinationName  string   `json:"destination_name"`
	DestinationRef   string   `json:"destination_ref"`
	NameTemplate     string   `json:"name_template"`
	ResolvedName     string   `json:"resolved_name"`
	ReservationNames []string `json:"reservation_names,omitempty"`
	Granularity      string   `json:"granularity"`
	Format           string   `json:"format"`
	DataMapping      string   `json:"data_mapping,omitempty"`
	DataKeyTemplate  string   `json:"data_key_template,omitempty"`
	DeleteMode       string   `json:"delete_mode"`
	Enabled          bool     `json:"enabled"`
	CreatedTime      string   `json:"created_time"`
	UpdatedTime      string   `json:"updated_time"`
}

func (record associationRecord) reservationName() string {
	if record.Granularity == syncGranularitySecretKey {
		reservationName, err := secretKeyReservationName(
			record.NameTemplate,
			record.Path,
			record.DestinationType,
			record.DestinationName,
		)
		if err == nil {
			return reservationName
		}
		return record.NameTemplate
	}
	return record.ResolvedName
}

func (record associationRecord) reservationNames() []string {
	names := []string{record.reservationName()}
	names = append(names, record.ReservationNames...)
	return uniqueSortedStrings(names)
}

type enqueueIntentRecord struct {
	Path          string                   `json:"path"`
	Generation    string                   `json:"generation"`
	Version       int                      `json:"version"`
	Operations    []enqueueIntentOperation `json:"operations"`
	Complete      bool                     `json:"complete"`
	CreatedTime   string                   `json:"created_time"`
	UpdatedTime   string                   `json:"updated_time"`
	CompletedTime string                   `json:"completed_time"`
}

type enqueueIntentOperation struct {
	ID             string               `json:"id"`
	Type           outbox.OperationType `json:"type"`
	AssociationID  string               `json:"association_id"`
	ObjectID       string               `json:"object_id"`
	DestinationRef string               `json:"destination_ref"`
}

type outboxRecord struct {
	ID               string               `json:"id"`
	Type             outbox.OperationType `json:"type"`
	Path             string               `json:"path"`
	Version          int                  `json:"version"`
	AssociationID    string               `json:"association_id"`
	ObjectID         string               `json:"object_id"`
	DestinationRef   string               `json:"destination_ref"`
	State            string               `json:"state"`
	Attempts         int                  `json:"attempts"`
	NotBefore        string               `json:"not_before"`
	CreatedTime      string               `json:"created_time"`
	UpdatedTime      string               `json:"updated_time"`
	IdempotencyKey   string               `json:"idempotency_key"`
	Trigger          string               `json:"trigger,omitempty"`
	ClaimOwner       string               `json:"claim_owner,omitempty"`
	ClaimExpiresTime string               `json:"claim_expires_time,omitempty"`
	ClaimAttempt     int                  `json:"claim_attempt,omitempty"`
}

type statusRecord struct {
	Path                  string `json:"path"`
	Version               int    `json:"version"`
	AssociationID         string `json:"association_id"`
	ObjectID              string `json:"object_id"`
	DestinationRef        string `json:"destination_ref"`
	ResolvedName          string `json:"resolved_name"`
	State                 string `json:"state"`
	PayloadSHA256         string `json:"payload_sha256"`
	RemoteVersion         string `json:"remote_version"`
	Verification          string `json:"verification,omitempty"`
	LastOperationID       string `json:"last_operation_id"`
	LastSuccessTime       string `json:"last_success_time"`
	LastReconcileTime     string `json:"last_reconcile_time,omitempty"`
	LastDriftDetectedTime string `json:"last_drift_detected_time,omitempty"`
	LastRepairTime        string `json:"last_repair_time,omitempty"`
	RepairCount           int    `json:"repair_count,omitempty"`
	LastErrorClass        string `json:"last_error_class"`
	LastError             string `json:"last_error"`
	UpdatedTime           string `json:"updated_time"`
}

func normalizeSourcePath(input string) (string, error) {
	path := strings.Trim(input, "/")
	if path == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("path contains invalid segment %q", part)
		}
		if part == "versions" {
			return "", fmt.Errorf("path contains reserved segment %q", part)
		}
	}
	switch parts[len(parts)-1] {
	case "plan", "disable", "enable", "sync":
		return "", fmt.Errorf("path must not end with reserved segment %q", parts[len(parts)-1])
	}
	return strings.Join(parts, "/"), nil
}

func metadataStorageKey(path string) string {
	return metadataStoragePrefix + path
}

func versionStorageKey(path string, version int) string {
	return versionStoragePrefix + path + "/versions/" + strconv.Itoa(version)
}

func enqueueIntentStorageKey(path string, version int) string {
	return enqueueIntentStoragePrefix + path + "/" + strconv.Itoa(version)
}

func outboxByStateStorageKey(state string, id string) string {
	return outboxByStateStoragePrefix + state + "/" + id
}

func destinationStorageKey(destinationType string, name string) string {
	return destinationStoragePrefix + destinationRef(destinationType, name)
}

func destinationSensitiveStorageKey(destinationType string, name string) string {
	return destinationSecretsPrefix + destinationRef(destinationType, name)
}

func associationStorageKey(path string, id string) string {
	return associationStoragePrefix + path + "/" + id
}

func associationByDestinationStorageKey(destinationType string, name string, id string) string {
	return associationByDestPrefix + destinationRef(destinationType, name) + "/" + id
}

func associationNameStorageKey(destinationRef string, resolvedName string, id string) string {
	return associationNamePrefix + destinationRef + "/" + nameReservationID(resolvedName) + "/" + id
}

func outboxStorageKey(id string) string {
	return outboxStoragePrefix + id
}

func outboxByPathStorageKey(path string, id string) string {
	return outboxByPathStoragePrefix + path + "/" + id
}

func outboxByDueStorageKey(dueTime string, id string) string {
	return outboxByDueStoragePrefix + dueTime + "/" + id
}

func statusStorageKey(path string, associationID string, objectID string) string {
	return statusStoragePrefix + path + "/" + associationID + "/" + objectID
}

func destinationRef(destinationType string, name string) string {
	return destinationType + "/" + name
}

func newAssociationID(
	path string,
	destinationType string,
	destinationName string,
	resolvedName string,
	granularity string,
) string {
	raw := fmt.Sprintf("%s:%s:%s:%s:%s", path, destinationType, destinationName, resolvedName, granularity)
	sum := sha256.Sum256([]byte(raw))
	return "assoc-" + hex.EncodeToString(sum[:8])
}

func nameReservationID(resolvedName string) string {
	sum := sha256.Sum256([]byte(resolvedName))
	return hex.EncodeToString(sum[:12])
}

func newOperationID(
	generation string,
	path string,
	version int,
	associationID string,
	objectID string,
	operationType outbox.OperationType,
) string {
	raw := fmt.Sprintf("%s:%s:%d:%s:%s:%s", generation, path, version, associationID, objectID, operationType)
	sum := sha256.Sum256([]byte(raw))
	return "op-" + hex.EncodeToString(sum[:8]) + "-" + strconv.Itoa(version)
}

func newOperationIDWithSalt(
	generation string,
	path string,
	version int,
	associationID string,
	objectID string,
	operationType outbox.OperationType,
	salt string,
) string {
	raw := fmt.Sprintf("%s:%s:%d:%s:%s:%s:%s", generation, path, version, associationID, objectID, operationType, salt)
	sum := sha256.Sum256([]byte(raw))
	return "op-" + hex.EncodeToString(sum[:8]) + "-" + strconv.Itoa(version)
}

func newMetadataRecord() metadataRecord {
	return metadataRecord{
		Generation:         bestEffortRuntimeID("gen"),
		MaxVersions:        defaultMaxVersions,
		DeleteVersionAfter: defaultDeleteVersionAfter,
		CustomMetadata:     make(map[string]string),
		Versions:           make(map[string]versionMetadata),
	}
}

func versionMetadataKey(version int) string {
	return strconv.Itoa(version)
}
