// Package image provides OCI registry access, content caching, and layer
// extraction for container images. It defines the [Repository] interface for
// fetching manifests and blobs, a [CachingRepository] wrapper that deduplicates
// and caches fetched content, and a [Handler] interface for format-specific
// content identification and unpacking.
package image
