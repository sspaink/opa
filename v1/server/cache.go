package server

import (
	"container/list"
	"sync"
)

type cache struct {
	data    map[string]any
	keylist []string
	idx     int
	maxSize int
	mtx     sync.RWMutex
}

func newCache(maxSize int) *cache {
	return &cache{
		data:    map[string]any{},
		keylist: []string{},
		maxSize: maxSize,
	}
}

func (c *cache) Get(k string) (any, bool) {
	c.mtx.RLock()
	v, ok := c.data[k]
	c.mtx.RUnlock()
	return v, ok
}

func (c *cache) Insert(k string, v any) {

	// Short path if its already in the cache
	_, ok := c.Get(k)
	if ok {
		return
	}

	// Slow path, grab the write lock and insert
	c.mtx.Lock()
	_, ok = c.data[k]
	if !ok {
		c.data[k] = v
		if len(c.keylist) < c.maxSize {
			// Haven't reached max size yet, keep adding keys.
			c.keylist = append(c.keylist, k)
		} else {
			// Start recycling spots in the key list and
			// dropping cache entries for them.
			delete(c.data, c.keylist[c.idx])
			c.keylist[c.idx] = k
			c.idx = (c.idx + 1) % c.maxSize
		}
	}
	c.mtx.Unlock()
}

// lruCache is a fixed-size cache that evicts the least recently used entry
// once full. It is safe for concurrent use.
type lruCache[K comparable, V any] struct {
	mtx     sync.Mutex
	maxSize int
	order   *list.List // front is most recently used
	items   map[K]*list.Element
}

type lruEntry[K comparable, V any] struct {
	key   K
	value V
}

func newLRUCache[K comparable, V any](maxSize int) *lruCache[K, V] {
	return &lruCache[K, V]{
		maxSize: maxSize,
		order:   list.New(),
		items:   make(map[K]*list.Element, maxSize),
	}
}

func (c *lruCache[K, V]) Get(k K) (V, bool) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if e, ok := c.items[k]; ok {
		c.order.MoveToFront(e)
		return e.Value.(*lruEntry[K, V]).value, true
	}
	var zero V
	return zero, false
}

func (c *lruCache[K, V]) Add(k K, v V) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if e, ok := c.items[k]; ok {
		e.Value.(*lruEntry[K, V]).value = v
		c.order.MoveToFront(e)
		return
	}
	c.items[k] = c.order.PushFront(&lruEntry[K, V]{key: k, value: v})
	if c.order.Len() > c.maxSize {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*lruEntry[K, V]).key)
	}
}

func (c *lruCache[K, V]) Purge() {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.order.Init()
	clear(c.items)
}
