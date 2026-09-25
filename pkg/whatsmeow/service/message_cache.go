package whatsmeow_service

import (
	"container/list"
	"sync"
	"time"
)

const (
	processedMessageCacheTTL = 10 * time.Minute
	maxProcessedMessageKeys  = 10_000
)

type processedMessageEntry struct {
	key       string
	expiresAt time.Time
}

// processedMessageCache bounds duplicate-receipt keys by both age and count.
type processedMessageCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	maxKeys int
	items   map[string]time.Time
	order   *list.List
}

func newProcessedMessageCache(maxKeys int, ttl time.Duration) *processedMessageCache {
	if maxKeys < 1 {
		maxKeys = 1
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	return &processedMessageCache{
		ttl:     ttl,
		maxKeys: maxKeys,
		items:   make(map[string]time.Time, maxKeys),
		order:   list.New(),
	}
}

// HasOrAdd returns true for a duplicate still inside dedupe window.
func (c *processedMessageCache) HasOrAdd(key string) bool {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.pruneExpired(now)
	if expiresAt, ok := c.items[key]; ok && expiresAt.After(now) {
		return true
	}
	delete(c.items, key)

	for len(c.items) >= c.maxKeys {
		c.evictOldest()
	}

	expiresAt := now.Add(c.ttl)
	c.items[key] = expiresAt
	c.order.PushBack(processedMessageEntry{key: key, expiresAt: expiresAt})
	return false
}

func (c *processedMessageCache) pruneExpired(now time.Time) {
	for element := c.order.Front(); element != nil; {
		next := element.Next()
		entry := element.Value.(processedMessageEntry)
		if entry.expiresAt.After(now) {
			break
		}
		if c.items[entry.key] == entry.expiresAt {
			delete(c.items, entry.key)
		}
		c.order.Remove(element)
		element = next
	}
}

func (c *processedMessageCache) evictOldest() {
	element := c.order.Front()
	if element == nil {
		return
	}
	entry := element.Value.(processedMessageEntry)
	if c.items[entry.key] == entry.expiresAt {
		delete(c.items, entry.key)
	}
	c.order.Remove(element)
}
