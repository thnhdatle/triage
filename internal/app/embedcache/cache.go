// SPDX-License-Identifier: Apache-2.0

package embedcache

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/thnhdatle/triage/internal/domain/difficulty"
)

type Stats struct {
	Hits   uint64
	Misses uint64
}

type entry struct {
	key [sha256.Size]byte
	vec []float32
}

// Cache is an LRU decorator around a difficulty.Embedder.
type Cache struct {
	next difficulty.Embedder
	size int

	mu    sync.Mutex
	ll    *list.List
	items map[[sha256.Size]byte]*list.Element
	stats Stats
}

var _ difficulty.Embedder = (*Cache)(nil)

// New returns a Cache holding up to size vectors.
func New(next difficulty.Embedder, size int) (*Cache, error) {
	if next == nil {
		return nil, errors.New("embedcache: nil embedder")
	}
	if size < 1 {
		return nil, fmt.Errorf("embedcache: size %d must be at least 1", size)
	}
	return &Cache{
		next:  next,
		size:  size,
		ll:    list.New(),
		items: make(map[[sha256.Size]byte]*list.Element, size),
	}, nil
}

func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Embed returns cached vectors and embeds only the missing texts.
func (c *Cache) Embed(ctx context.Context, model string, dimensions int, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	keys := make([][sha256.Size]byte, len(texts))
	var missIdx []int
	var missTexts []string

	c.mu.Lock()
	for i, text := range texts {
		keys[i] = cacheKey(model, dimensions, text)
		if el, ok := c.items[keys[i]]; ok {
			c.ll.MoveToFront(el)
			out[i] = slices.Clone(el.Value.(*entry).vec) //nolint:forcetypeassert // list only holds *entry.
			c.stats.Hits++
			continue
		}
		c.stats.Misses++
		missIdx = append(missIdx, i)
		missTexts = append(missTexts, text)
	}
	c.mu.Unlock()

	if len(missTexts) == 0 {
		return out, nil
	}
	vecs, err := c.next.Embed(ctx, model, dimensions, missTexts)
	if err != nil {
		return nil, fmt.Errorf("embedcache: upstream embed: %w", err)
	}
	if len(vecs) != len(missTexts) {
		return nil, fmt.Errorf("embedcache: upstream returned %d vectors for %d texts", len(vecs), len(missTexts))
	}

	c.mu.Lock()
	for j, i := range missIdx {
		c.store(keys[i], vecs[j])
		out[i] = slices.Clone(vecs[j])
	}
	c.mu.Unlock()
	return out, nil
}

func (c *Cache) store(key [sha256.Size]byte, vec []float32) {
	if el, ok := c.items[key]; ok {
		el.Value.(*entry).vec = slices.Clone(vec) //nolint:forcetypeassert // list only holds *entry.
		c.ll.MoveToFront(el)
		return
	}
	c.items[key] = c.ll.PushFront(&entry{key: key, vec: slices.Clone(vec)})
	for c.ll.Len() > c.size {
		oldest := c.ll.Back()
		c.ll.Remove(oldest)
		delete(c.items, oldest.Value.(*entry).key) //nolint:forcetypeassert // list only holds *entry.
	}
}

func cacheKey(model string, dimensions int, text string) [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(dimensions)))
	h.Write([]byte{0})
	h.Write([]byte(text))
	var k [sha256.Size]byte
	h.Sum(k[:0])
	return k
}
