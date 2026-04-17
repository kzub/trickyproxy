package negativecache

import (
	"sync"
	"testing"
	"time"
)

func TestSetAndGet(t *testing.T) {
	cache := New(1 * time.Second)

	cache.Set("test-key")

	if !cache.Get("test-key") {
		t.Error("Expected Get to return true for recently set key")
	}
}

func TestGetExpired(t *testing.T) {
	cache := New(50 * time.Millisecond)

	cache.Set("expire-key")

	time.Sleep(60 * time.Millisecond)

	if cache.Get("expire-key") {
		t.Error("Expected Get to return false for expired key")
	}
}

func TestGetNonexistent(t *testing.T) {
	cache := New(1 * time.Second)

	if cache.Get("nonexistent-key") {
		t.Error("Expected Get to return false for nonexistent key")
	}
}

func TestDisabledCache(t *testing.T) {
	cache := New(0)

	if cache.Enabled() {
		t.Error("Expected Enabled to return false when TTL is 0")
	}

	cache.Set("test-key")

	if cache.Get("test-key") {
		t.Error("Expected Get to return false in disabled cache")
	}
}

func TestDifferentKeys(t *testing.T) {
	cache := New(1 * time.Second)

	cache.Set("key-a")

	if cache.Get("key-b") {
		t.Error("Expected Get to return false for key-b")
	}

	if !cache.Get("key-a") {
		t.Error("Expected Get to return true for key-a")
	}
}

func TestConcurrentAccess(t *testing.T) {
	cache := New(1 * time.Second)
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := "concurrent-key"
			cache.Set(key)
			cache.Get(key)
		}(i)
	}

	wg.Wait()
}
