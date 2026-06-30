// Package outbox contains durable sync operation types.
package outbox

// OperationType describes the remote mutation requested by an outbox item.
type OperationType string

const (
	OperationTypeUpsert OperationType = "upsert"
	OperationTypeDelete OperationType = "delete"
)

// Operation is the durable unit of sync work. Storage fields are added as the
// queue implementation lands.
type Operation struct {
	ID      string
	Type    OperationType
	Path    string
	Version int
}
