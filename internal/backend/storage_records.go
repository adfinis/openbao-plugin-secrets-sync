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
	metadataStoragePrefix      = "metadata/"
	versionStoragePrefix       = "data/"
	enqueueIntentStoragePrefix = "enqueue_intent/"
	outboxStoragePrefix        = "outbox/"
	outboxByPathStoragePrefix  = "outbox_by_path/"
	defaultQueueCapacity       = 1000
	defaultMaxVersions         = 10
	outboxStatePending         = "pending"
	outboxStateRetryWait       = "retry_wait"
	outboxStateFailedTerminal  = "failed_terminal"
	fakeAssociationID          = "local-pending"
	fakeDestinationRef         = "fake/default"
	syncObjectIDSecretPath     = "secret-path"
)

type secretPayload map[string]interface{} //nolint:forbidigo // OpenBao SDK TypeMap uses map[string]interface{}.

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
	CurrentVersion int                        `json:"current_version"`
	OldestVersion  int                        `json:"oldest_version"`
	MaxVersions    int                        `json:"max_versions"`
	CASRequired    bool                       `json:"cas_required"`
	Versions       map[string]versionMetadata `json:"versions"`
	UpdatedTime    string                     `json:"updated_time"`
}

type enqueueIntentRecord struct {
	Path          string   `json:"path"`
	Version       int      `json:"version"`
	OperationIDs  []string `json:"operation_ids"`
	Complete      bool     `json:"complete"`
	CreatedTime   string   `json:"created_time"`
	UpdatedTime   string   `json:"updated_time"`
	CompletedTime string   `json:"completed_time"`
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

func outboxStorageKey(id string) string {
	return outboxStoragePrefix + id
}

func outboxByPathStorageKey(path string, id string) string {
	return outboxByPathStoragePrefix + path + "/" + id
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
		MaxVersions: defaultMaxVersions,
		Versions:    make(map[string]versionMetadata),
	}
}

func versionMetadataKey(version int) string {
	return strconv.Itoa(version)
}
