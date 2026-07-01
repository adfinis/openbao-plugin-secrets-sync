package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/adfinis/openbao-secret-sync/internal/outbox"
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
	outboxByPathStoragePrefix  = "outbox_by_path/"
	defaultQueueCapacity       = 1000
	defaultMaxVersions         = 10
	defaultDeleteVersionAfter  = "0s"
	outboxStatePending         = "pending"
	outboxStateRetryWait       = "retry_wait"
	outboxStateFailedTerminal  = "failed_terminal"
	outboxStateSucceeded       = "succeeded"
	outboxStateCanceled        = "canceled"
	syncGranularitySecretPath  = "secret-path"
	syncGranularitySecretKey   = "secret-key"
	syncObjectIDSecretPath     = syncGranularitySecretPath
	statusStoragePrefix        = "status/"
	providerTypeFake           = "fake"
	defaultAssociationFormat   = "json"
	rawAssociationFormat       = "raw"
	defaultNameTemplate        = "{{ path }}"
	defaultPerKeyNameTemplate  = "{{ path }}/{{ key }}"
	defaultDeleteMode          = deleteModeRetain
	deleteModeRetain           = "retain"
	deleteModeDelete           = "delete"
	deleteModeOrphan           = "orphan"
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
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Disabled    bool              `json:"disabled"`
	Config      map[string]string `json:"config"`
	CreatedTime string            `json:"created_time"`
	UpdatedTime string            `json:"updated_time"`
}

type destinationSensitiveRecord struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Config      map[string]string `json:"config"`
	CreatedTime string            `json:"created_time"`
	UpdatedTime string            `json:"updated_time"`
}

type associationRecord struct {
	ID              string `json:"id"`
	Path            string `json:"path"`
	DestinationType string `json:"destination_type"`
	DestinationName string `json:"destination_name"`
	DestinationRef  string `json:"destination_ref"`
	NameTemplate    string `json:"name_template"`
	ResolvedName    string `json:"resolved_name"`
	Granularity     string `json:"granularity"`
	Format          string `json:"format"`
	DeleteMode      string `json:"delete_mode"`
	Enabled         bool   `json:"enabled"`
	CreatedTime     string `json:"created_time"`
	UpdatedTime     string `json:"updated_time"`
}

type enqueueIntentRecord struct {
	Path          string                   `json:"path"`
	Version       int                      `json:"version"`
	OperationIDs  []string                 `json:"operation_ids"`
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
	ID             string               `json:"id"`
	Type           outbox.OperationType `json:"type"`
	Path           string               `json:"path"`
	Version        int                  `json:"version"`
	AssociationID  string               `json:"association_id"`
	ObjectID       string               `json:"object_id"`
	DestinationRef string               `json:"destination_ref"`
	State          string               `json:"state"`
	Attempts       int                  `json:"attempts"`
	NotBefore      string               `json:"not_before"`
	CreatedTime    string               `json:"created_time"`
	UpdatedTime    string               `json:"updated_time"`
	IdempotencyKey string               `json:"idempotency_key"`
}

type statusRecord struct {
	Path            string `json:"path"`
	Version         int    `json:"version"`
	AssociationID   string `json:"association_id"`
	ObjectID        string `json:"object_id"`
	DestinationRef  string `json:"destination_ref"`
	ResolvedName    string `json:"resolved_name"`
	State           string `json:"state"`
	PayloadSHA256   string `json:"payload_sha256"`
	RemoteVersion   string `json:"remote_version"`
	LastOperationID string `json:"last_operation_id"`
	LastSuccessTime string `json:"last_success_time"`
	LastErrorClass  string `json:"last_error_class"`
	LastError       string `json:"last_error"`
	UpdatedTime     string `json:"updated_time"`
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
	path string,
	version int,
	associationID string,
	objectID string,
	operationType outbox.OperationType,
) string {
	raw := fmt.Sprintf("%s:%d:%s:%s:%s", path, version, associationID, objectID, operationType)
	sum := sha256.Sum256([]byte(raw))
	return "op-" + hex.EncodeToString(sum[:8]) + "-" + strconv.Itoa(version)
}

func newMetadataRecord() metadataRecord {
	return metadataRecord{
		MaxVersions:        defaultMaxVersions,
		DeleteVersionAfter: defaultDeleteVersionAfter,
		CustomMetadata:     make(map[string]string),
		Versions:           make(map[string]versionMetadata),
	}
}

func versionMetadataKey(version int) string {
	return strconv.Itoa(version)
}
