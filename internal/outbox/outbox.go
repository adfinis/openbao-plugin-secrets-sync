// Package outbox contains durable sync operation enums.
package outbox

// OperationType describes the remote mutation requested by a stored queue item.
type OperationType string

const (
	OperationTypeUpsert OperationType = "upsert"
	OperationTypeDelete OperationType = "delete"
)
