package server

import (
	"strconv"
	"sync"
	"testing"
)

func TestCacheBase(t *testing.T) {
	c := newCache(5)
	foo := struct{}{}
	c.Insert("foo", foo)
	ensureCacheKey(t, c, "foo", foo)
}

func TestCacheLimit(t *testing.T) {
	max := 10
	c := newCache(max)

	// Fill the cache with values
	var i int
	for i = 0; i < max; i++ {
		c.Insert(strconv.Itoa(i), i)
	}

	// Ensure its at the max size
	ensureCacheSize(t, c, max)

	// Ensure they are all stored..
	for j := range max {
		ensureCacheKey(t, c, strconv.Itoa(j), j)
	}

	// Continue filling the cache, expect old keys to be dropped
	c.Insert(strconv.Itoa(i), i)
	ensureCacheKey(t, c, strconv.Itoa(i), i)

	// Should still be at max size
	ensureCacheSize(t, c, max)

	// Expect that "0" got dropped
	_, ok := c.Get("0")
	if ok {
		t.Fatal("Expected key '0' to not be found")
	}

	// Load the cache with many more than max
	for ; i < max*20; i++ {
		c.Insert(strconv.Itoa(i), i)
	}
	ensureCacheSize(t, c, max)

	// Ensure the last set of "max" number are available, and everything else is not
	for j := range i {
		k := strconv.Itoa(j)
		if j >= (i - max) {
			ensureCacheKey(t, c, k, j)
		} else {
			_, ok := c.Get(k)
			if ok {
				t.Fatalf("Expected key %s to not be found", k)
			}
		}
	}
}

func ensureCacheKey(t *testing.T, c *cache, k string, v any) {
	t.Helper()
	actual, ok := c.Get(k)
	if !ok || v != actual {
		t.Fatalf("expected to retrieve value %v for key %s, got %v ok==%t", v, k, actual, ok)
	}
}

func ensureCacheSize(t *testing.T, c *cache, size int) {
	if len(c.data) != size && len(c.keylist) != size {
		t.Fatalf("Unexpected cache size len(data)=%d len(keylist)=%d, expected %d", size, len(c.data), len(c.keylist))
	}
}

func TestLRUCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := newLRUCache[string, int](3)
	c.Add("a", 1)
	c.Add("b", 2)
	c.Add("c", 3)

	// Reading "a" makes "b" the least recently used entry.
	ensureLRUCacheKey(t, c, "a", 1)
	c.Add("d", 4)

	if _, ok := c.Get("b"); ok {
		t.Fatal("expected key 'b' to be evicted")
	}
	for k, v := range map[string]int{"a": 1, "c": 3, "d": 4} {
		ensureLRUCacheKey(t, c, k, v)
	}
	if c.order.Len() != 3 || len(c.items) != 3 {
		t.Fatalf("expected size 3, got len(order)=%d len(items)=%d", c.order.Len(), len(c.items))
	}
}

func TestLRUCacheAddExistingKey(t *testing.T) {
	c := newLRUCache[string, int](2)
	c.Add("a", 1)
	c.Add("b", 2)

	// Re-adding "a" updates its value and makes "b" the least recently used entry.
	c.Add("a", 10)
	c.Add("c", 3)

	if _, ok := c.Get("b"); ok {
		t.Fatal("expected key 'b' to be evicted")
	}
	ensureLRUCacheKey(t, c, "a", 10)
	ensureLRUCacheKey(t, c, "c", 3)
}

func TestLRUCachePurge(t *testing.T) {
	c := newLRUCache[string, int](2)
	c.Add("a", 1)
	c.Add("b", 2)
	c.Purge()

	if _, ok := c.Get("a"); ok {
		t.Fatal("expected key 'a' to be purged")
	}
	if c.order.Len() != 0 || len(c.items) != 0 {
		t.Fatalf("expected empty cache, got len(order)=%d len(items)=%d", c.order.Len(), len(c.items))
	}

	c.Add("c", 3)
	ensureLRUCacheKey(t, c, "c", 3)
}

func TestLRUCacheConcurrentAccess(t *testing.T) {
	c := newLRUCache[string, int](10)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 1000 {
				k := strconv.Itoa((i + j) % 20)
				c.Add(k, j)
				c.Get(k)
				if j%100 == 0 {
					c.Purge()
				}
			}
		})
	}
	wg.Wait()

	if c.order.Len() > 10 || c.order.Len() != len(c.items) {
		t.Fatalf("inconsistent cache, len(order)=%d len(items)=%d", c.order.Len(), len(c.items))
	}
}

func ensureLRUCacheKey(t *testing.T, c *lruCache[string, int], k string, v int) {
	t.Helper()
	actual, ok := c.Get(k)
	if !ok || v != actual {
		t.Fatalf("expected to retrieve value %v for key %s, got %v ok==%t", v, k, actual, ok)
	}
}
