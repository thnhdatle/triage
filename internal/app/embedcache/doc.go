// SPDX-License-Identifier: Apache-2.0

// Package embedcache provides an LRU difficulty.Embedder decorator (mf strategy).
//
// The key of an entry is SHA-256 over the embedding model, the requested dimensions and the text.
// The dimension is fixed at 1536 for mf, so adding it to the key is a
// safe superset that also keeps other dimensions apart. A batch is looked up text by text and the
// upstream embedder is called once with the misses only, in order. The cache is bounded (least
// recently used entries are evicted), safe for concurrent use, never holds its lock during the
// upstream call, never caches errors and hands out copies of vectors.
//
//	cache, err := embedcache.New(upstream, 1024)
//	if err != nil {
//		return err
//	}
//	vecs, err := cache.Embed(ctx, "text-embedding-3-small", 1536, []string{"hello"})
//	hits := cache.Stats().Hits
package embedcache
