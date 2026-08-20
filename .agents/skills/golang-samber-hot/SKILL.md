---
name: golang-samber-hot
description: Use samber/hot for bounded in-memory caches with eviction, TTL, stale revalidation, loaders, sharding, missing-key caching, copying, and metrics.
---

# samber/hot

This guidance targets github.com/samber/hot v0.13 APIs; verify the pinned module before copying examples. hot is an in-memory cache, not a durable store or distributed-coherence mechanism.

## Build a cache

NewHotCache returns a HotCacheConfig, not a cache and not a second return value:

~~~go
cache := hot.NewHotCache[string, User](hot.LRU, 10_000).
    WithTTL(5 * time.Minute).
    WithLoaders(func(keys []string) (map[string]User, error) {
        return loadUsers(keys)
    }).
    Build()

user, found, err := cache.Get("user-42")
~~~

The loader type is func(keys []K) (found map[K]V, err error). Missing keys are distinct from a found zero value. Use WithMissingSharedCache or WithMissingCache when negative caching is intended, and choose a shorter TTL when absence can change.

Build validates combinations such as janitor with locking. WithoutLocking is only for a cache that is never accessed concurrently and cannot be combined with WithJanitor. Use a positive TTL when expiry is required; do not assume zero means the same thing across versions without checking.

## Expiry and loaders

WithTTL establishes the normal expiry. WithRevalidation(stale, loaders...) serves stale entries during the stale window and refreshes them in the background. Decide whether revalidation failure drops or keeps an old value according to the package's policy and your data freshness requirements.

Get and GetMany return errors from loaders. A loader chain may fill only some requested keys; handle the found/missing result deliberately. The cache deduplicates concurrent loads for a key, but that does not make the loader idempotent or safe to run without a context/deadline of its own.

WithJitter(lambda, upperBound) applies an exponential random variation bounded to [0, upperBound); it is not uniform jitter and the resulting TTL is not simply TTL plus a fixed random amount. Measure stampede behavior rather than promising a fixed improvement.

## Eviction and concurrency

Select LRU, LFU, TinyLFU, W-TinyLFU, 2Q, ARC, FIFO, SIEVE, or another algorithm based on measured access patterns. Capacity is an item count, not a memory budget. Estimate value size and enforce a separate memory policy if values are large.

The cache's safe/default locking protects cache operations, but it does not make a mutable V safe after return. WithCopyOnRead and WithCopyOnWrite receive and return V; use a real deep copy for maps, slices, pointers, or nested objects. A shallow struct copy only copies references and does not isolate nested mutable state.

Janitor starts background cleanup and must be stopped with StopJanitor when the cache lifetime ends. Eviction callbacks run synchronously in the current implementation; keep them short and non-blocking or treat them as part of cache latency.

## Metrics and review

WithPrometheusMetrics(name) enables collectors. Current names include hot_hit_total, hot_miss_total, hot_insertion_total, hot_eviction_total{reason=...}, hot_size_bytes, hot_length, and hot_settings_* gauges. Register collectors with the application's registry according to the package API; do not invent names from an old dashboard.

Review capacity, TTL, negative-cache semantics, stale serving, copy functions, loader timeout/retry, janitor shutdown, and metrics cardinality. Cache only data that can be recomputed or invalidated safely. Never use cached authorization decisions without an explicit freshness and revocation design.
