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
) (providers.DestinationRuntime, func(context.Context), error) {
	if b.cachingDisabled() {
		b.clearDestinationRuntimes(ctx)
		runtime, err := openDestinationRuntime(ctx, provider, cfg)
		return runtimeWithRelease(runtime, err)
	}
	return b.cachedDestinationRuntime(ctx, provider, record, cfg)
}

func (b *secretSyncBackend) cachedDestinationRuntime(
	ctx context.Context,
	provider providers.Provider,
	record destinationRecord,
	cfg providers.DestinationConfig,
) (providers.DestinationRuntime, func(context.Context), error) {
	ref := destinationRef(record.Type, record.Name)
	fingerprint := destinationConfigFingerprint(cfg)
	for {
		b.cacheMu.Lock()
		b.ensureDestinationRuntimeCacheLocked()
		if entry, ok := b.runtimeCache[ref]; ok && entry.fingerprint == fingerprint {
			runtime := entry.runtime
			b.cacheMu.Unlock()
			return runtime, keepDestinationRuntime, nil
		}
		if build, ok := b.runtimeBuilds[ref]; ok {
			ready := build.ready
			b.cacheMu.Unlock()
			select {
			case <-ready:
				if build.err != nil {
					return nil, nil, build.err
				}
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		build := &destinationRuntimeBuild{ready: make(chan struct{})}
		b.runtimeBuilds[ref] = build
		cacheEpoch := b.runtimeCacheEpoch
		destinationEpoch := b.runtimeDestinationEpochs[ref]
		b.cacheMu.Unlock()

		// Opening a provider destination can perform I/O, so it happens outside
		// cacheMu. Epochs capture cache-wide clears and per-destination
		// invalidations that occur while the runtime is being built.
		runtime, err := openDestinationRuntime(ctx, provider, cfg)
		return b.finishDestinationRuntimeBuild(
			ctx,
			ref,
			fingerprint,
			build,
			runtime,
			err,
			cacheEpoch,
			destinationEpoch,
		)
	}
}

func openDestinationRuntime(
	ctx context.Context,
	provider providers.Provider,
	cfg providers.DestinationConfig,
) (providers.DestinationRuntime, error) {
	runtime, err := provider.OpenDestination(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, &providers.Error{
			Class:   providers.ErrorClassInternal,
			Message: "provider returned nil destination runtime",
		}
	}
	return runtime, nil
}

func (b *secretSyncBackend) finishDestinationRuntimeBuild(
	ctx context.Context,
	ref string,
	fingerprint string,
	build *destinationRuntimeBuild,
	runtime providers.DestinationRuntime,
	buildErr error,
	cacheEpoch uint64,
	destinationEpoch uint64,
) (providers.DestinationRuntime, func(context.Context), error) {
	var staleRuntime providers.DestinationRuntime
	cached := false
	b.cacheMu.Lock()
	if buildErr == nil &&
		cacheEpoch == b.runtimeCacheEpoch &&
		destinationEpoch == b.runtimeDestinationEpochs[ref] {
		// Publish only if no invalidation raced with the build. Otherwise the
		// runtime is returned to this caller but not cached for future calls.
		if old, ok := b.runtimeCache[ref]; ok {
			staleRuntime = old.runtime
		}
		b.runtimeCache[ref] = destinationRuntimeCacheEntry{
			fingerprint: fingerprint,
			runtime:     runtime,
		}
		cached = true
	}
	build.err = buildErr
	if currentBuild, ok := b.runtimeBuilds[ref]; ok && currentBuild == build {
		delete(b.runtimeBuilds, ref)
	}
	close(build.ready)
	b.cacheMu.Unlock()
	closeDestinationRuntime(ctx, staleRuntime)

	if buildErr != nil {
		return nil, nil, buildErr
	}
	if cached {
		return runtime, keepDestinationRuntime, nil
	}
	return runtimeWithRelease(runtime, nil)
}

func runtimeWithRelease(
	runtime providers.DestinationRuntime,
	err error,
) (providers.DestinationRuntime, func(context.Context), error) {
	if err != nil {
		return nil, nil, err
	}
	return runtime, func(releaseCtx context.Context) {
		closeDestinationRuntime(releaseCtx, runtime)
	}, nil
}

func (b *secretSyncBackend) cachingDisabled() bool {
	return b.Backend != nil && b.System() != nil && b.System().CachingDisabled()
}

func keepDestinationRuntime(context.Context) {}

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
