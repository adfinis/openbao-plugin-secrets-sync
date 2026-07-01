package backend

import "github.com/openbao/openbao/sdk/v2/helper/locksutil"

const (
	sourceWriteLockPrefix      = "source:"
	associationWriteLockPrefix = "association-name:"
)

func (b *secretSyncBackend) lockSourcePath(path string) func() {
	return b.lockWriteKeys(sourceWriteLockKey(path))
}

func (b *secretSyncBackend) lockSourcePathAndAssociationName(
	path string,
	destinationRef string,
	reservationName string,
) func() {
	return b.lockWriteKeys(
		sourceWriteLockKey(path),
		associationNameWriteLockKey(destinationRef, reservationName),
	)
}

func (b *secretSyncBackend) lockWriteKeys(keys ...string) func() {
	locks := locksutil.LocksForKeys(b.writeLocks, keys)
	for _, lock := range locks {
		lock.Lock()
	}
	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].Unlock()
		}
	}
}

func sourceWriteLockKey(path string) string {
	return sourceWriteLockPrefix + path
}

func associationNameWriteLockKey(destinationRef string, reservationName string) string {
	return associationWriteLockPrefix + destinationRef + ":" + reservationName
}
