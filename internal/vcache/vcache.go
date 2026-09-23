// Package vcache keeps recently used file versions in memory, bounded by
// total bytes, evicting the least recently used first.
package vcache

import (
	"container/list"
	"sync"
)

// DefaultMaxBytes bounds a cache created with New(0).
const DefaultMaxBytes = 64 << 20

type key struct{ path, version string }

type entry struct {
	key  key
	data []byte
	meta Meta
}

// Meta is what history knows about a cached version: where the file was, the
// first change made after it, and the newest change id the entry reflects.
type Meta struct {
	Path string
	Next int64
	AsOf int64
}

// Cache is an LRU of file contents keyed by root-relative path and version.
// It is safe for concurrent use. Stored slices must not be modified.
type Cache struct {
	mu    sync.Mutex
	max   int64
	size  int64
	order *list.List // front = most recent
	items map[key]*list.Element
}

// New returns a cache holding at most maxBytes of content; <= 0 uses DefaultMaxBytes.
func New(maxBytes int64) *Cache {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &Cache{max: maxBytes, order: list.New(), items: map[key]*list.Element{}}
}

// Get returns the content of path at version, if cached.
func (c *Cache) Get(path, version string) ([]byte, bool) {
	data, _, ok := c.GetMeta(path, version)
	return data, ok
}

// GetMeta returns the content and meta of path at version, if cached.
func (c *Cache) GetMeta(path, version string) ([]byte, Meta, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key{path, version}]
	if !ok {
		return nil, Meta{}, false
	}
	c.order.MoveToFront(el)
	e := el.Value.(*entry)
	return e.data, e.meta, true
}

// Put stores data as path at version with zero meta. Content larger than the
// cache is ignored.
func (c *Cache) Put(path, version string, data []byte) { c.PutMeta(path, version, data, Meta{}) }

// PutMeta stores data and m as path at version.
func (c *Cache) PutMeta(path, version string, data []byte, m Meta) {
	n := int64(len(data))
	if n > c.max {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key{path, version}
	if el, ok := c.items[k]; ok {
		e := el.Value.(*entry)
		c.size += n - int64(len(e.data))
		e.data, e.meta = data, m
		c.order.MoveToFront(el)
	} else {
		c.items[k] = c.order.PushFront(&entry{key: k, data: data, meta: m})
		c.size += n
	}
	for c.size > c.max {
		c.remove(c.order.Back())
	}
}

// Remove drops path at version.
func (c *Cache) Remove(path, version string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key{path, version}]; ok {
		c.remove(el)
	}
}

var byRoot sync.Map // canonical root -> *Cache

// Register makes c the cache of root for For; nil unregisters.
func Register(root string, c *Cache) {
	if c == nil {
		byRoot.Delete(root)
		return
	}
	byRoot.Store(root, c)
}

// For returns the cache registered for root (by the live process), or nil.
func For(root string) *Cache {
	c, _ := byRoot.Load(root)
	cc, _ := c.(*Cache)
	return cc
}

// Forget drops every cached version of path.
func (c *Cache) Forget(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for el := c.order.Front(); el != nil; {
		next := el.Next()
		if el.Value.(*entry).key.path == path {
			c.remove(el)
		}
		el = next
	}
}

// Len returns the number of cached versions.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Size returns the cached content size in bytes.
func (c *Cache) Size() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.size
}

func (c *Cache) remove(el *list.Element) {
	e := c.order.Remove(el).(*entry)
	delete(c.items, e.key)
	c.size -= int64(len(e.data))
}
