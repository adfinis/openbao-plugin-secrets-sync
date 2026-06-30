// Package store wraps OpenBao logical storage with typed sync records.
package store

import "github.com/openbao/openbao/sdk/v2/logical"

// Store is the storage boundary for backend services.
type Store struct {
	storage logical.Storage
}

// New returns a typed storage wrapper.
func New(storage logical.Storage) Store {
	return Store{storage: storage}
}

// Ready reports whether the store has a storage backend.
func (s Store) Ready() bool {
	return s.storage != nil
}
