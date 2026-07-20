package backend

import (
	"context"

	"github.com/openbao/openbao/sdk/v2/logical"
)

// withEnqueueMutationTransaction uses OpenBao's native transactional storage
// contract when available. Non-transactional storage keeps the existing
// process-local queue serialization and ordered recovery path.
func (b *secretSyncBackend) withEnqueueMutationTransaction(
	ctx context.Context,
	storage logical.Storage,
	mutation func(logical.Storage) error,
) error {
	if _, transactional := storage.(logical.TransactionalStorage); !transactional {
		b.enqueueMu.Lock()
		defer b.enqueueMu.Unlock()
	}
	return logical.WithTransaction(ctx, storage, mutation)
}
