package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
)

type destinationRuntimeCacheEntry struct {
	fingerprint string
	runtime     providers.DestinationRuntime
}

type destinationRuntimeBuild struct {
	ready chan struct{}
	err   error
}

func (b *secretSyncBackend) destinationRuntime(
	ctx context.Context,
	provider providers.Provider,
	record destinationRecord,
	cfg providers.DestinationConfig,
) (providers.DestinationRuntime, error) {
	ref := destinationRef(record.Type, record.Name)
	fingerprint := destinationConfigFingerprint(cfg)
	for {
		b.cacheMu.Lock()
		b.ensureDestinationRuntimeCacheLocked()
		if entry, ok := b.runtimeCache[ref]; ok && entry.fingerprint == fingerprint {
			runtime := entry.runtime
			b.cacheMu.Unlock()
			return runtime, nil
		}
		if build, ok := b.runtimeBuilds[ref]; ok {
			ready := build.ready
			b.cacheMu.Unlock()
			select {
			case <-ready:
				if build.err != nil {
					return nil, build.err
				}
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		build := &destinationRuntimeBuild{ready: make(chan struct{})}
		b.runtimeBuilds[ref] = build
		cacheEpoch := b.runtimeCacheEpoch
		destinationEpoch := b.runtimeDestinationEpochs[ref]
		b.cacheMu.Unlock()

		runtime, err := provider.OpenDestination(ctx, cfg)
		if err == nil && runtime == nil {
			err = &providers.Error{
				Class:   providers.ErrorClassInternal,
				Message: "provider returned nil destination runtime",
			}
		}

		var staleRuntime providers.DestinationRuntime
		b.cacheMu.Lock()
		if err == nil &&
			cacheEpoch == b.runtimeCacheEpoch &&
			destinationEpoch == b.runtimeDestinationEpochs[ref] {
			if old, ok := b.runtimeCache[ref]; ok {
				staleRuntime = old.runtime
			}
			b.runtimeCache[ref] = destinationRuntimeCacheEntry{
				fingerprint: fingerprint,
				runtime:     runtime,
			}
		}
		build.err = err
		delete(b.runtimeBuilds, ref)
		close(build.ready)
		b.cacheMu.Unlock()
		closeDestinationRuntime(ctx, staleRuntime)
		return runtime, err
	}
}

func (b *secretSyncBackend) invalidateDestinationRuntime(ctx context.Context, ref string) {
	var runtime providers.DestinationRuntime
	b.cacheMu.Lock()
	b.ensureDestinationRuntimeCacheLocked()
	if entry, ok := b.runtimeCache[ref]; ok {
		runtime = entry.runtime
		delete(b.runtimeCache, ref)
	}
	b.runtimeDestinationEpochs[ref]++
	b.cacheMu.Unlock()
	closeDestinationRuntime(ctx, runtime)
}

func (b *secretSyncBackend) clearDestinationRuntimes(ctx context.Context) {
	b.cacheMu.Lock()
	b.ensureDestinationRuntimeCacheLocked()
	runtimes := make([]providers.DestinationRuntime, 0, len(b.runtimeCache))
	for _, entry := range b.runtimeCache {
		runtimes = append(runtimes, entry.runtime)
	}
	b.runtimeCache = map[string]destinationRuntimeCacheEntry{}
	b.runtimeBuilds = map[string]*destinationRuntimeBuild{}
	b.runtimeDestinationEpochs = map[string]uint64{}
	b.runtimeCacheEpoch++
	b.cacheMu.Unlock()
	for _, runtime := range runtimes {
		closeDestinationRuntime(ctx, runtime)
	}
}

func (b *secretSyncBackend) ensureDestinationRuntimeCacheLocked() {
	if b.runtimeCache == nil {
		b.runtimeCache = map[string]destinationRuntimeCacheEntry{}
	}
	if b.runtimeBuilds == nil {
		b.runtimeBuilds = map[string]*destinationRuntimeBuild{}
	}
	if b.runtimeDestinationEpochs == nil {
		b.runtimeDestinationEpochs = map[string]uint64{}
	}
}

func closeDestinationRuntime(ctx context.Context, runtime providers.DestinationRuntime) {
	if runtime == nil {
		return
	}
	_ = runtime.Close(ctx)
}

func destinationConfigFingerprint(cfg providers.DestinationConfig) string {
	config := cfg.Config
	if config == nil {
		config = map[string]string{}
	}
	payload, err := json.Marshal(struct {
		Name   string            `json:"name"`
		Config map[string]string `json:"config"`
	}{
		Name:   cfg.Name,
		Config: config,
	})
	if err != nil {
		sum := sha256.Sum256([]byte(cfg.Name))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func destinationRefFromInvalidationKey(key string) (string, bool) {
	for _, prefix := range []string{destinationStoragePrefix, destinationSecretsPrefix} {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		ref := strings.TrimPrefix(key, prefix)
		destinationType, name, ok := strings.Cut(ref, "/")
		if !ok || destinationType == "" || name == "" || strings.Contains(name, "/") {
			return "", false
		}
		return destinationRef(destinationType, name), true
	}
	return "", false
}
