package whatsmeow_service

import (
	"testing"
	"time"
)

func TestProcessedMessageCacheExpiresEntries(t *testing.T) {
	cache := newProcessedMessageCache(10, 5*time.Millisecond)
	if cache.HasOrAdd("receipt") {
		t.Fatal("first receipt was marked as duplicate")
	}
	time.Sleep(10 * time.Millisecond)
	if cache.HasOrAdd("receipt") {
		t.Fatal("expired receipt remained a duplicate")
	}
}

func TestProcessedMessageCacheCapsEntries(t *testing.T) {
	cache := newProcessedMessageCache(2, time.Hour)
	cache.HasOrAdd("one")
	cache.HasOrAdd("two")
	if !cache.HasOrAdd("one") {
		t.Fatal("receipt inside dedupe window was not detected")
	}
	cache.HasOrAdd("three")
	if cache.HasOrAdd("one") {
		t.Fatal("oldest receipt was not evicted at capacity")
	}
	if len(cache.items) != 2 {
		t.Fatalf("cache size = %d, want 2", len(cache.items))
	}
}
