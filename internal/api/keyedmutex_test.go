package api

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeyedMutex_SameKeyIsSerialized(t *testing.T) {
	var km keyedMutex
	var running, peak int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := km.lock("tenant\x00user\x00general")
			defer unlock()
			n := atomic.AddInt32(&running, 1)
			for {
				p := atomic.LoadInt32(&peak)
				if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt32(&running, -1)
		}()
	}
	wg.Wait()
	if peak != 1 {
		t.Fatalf("same key must never run concurrently, peak=%d", peak)
	}
}

func TestKeyedMutex_DifferentKeysRunInParallel(t *testing.T) {
	var km keyedMutex
	var running, peak int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, key := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			unlock := km.lock(key)
			defer unlock()
			n := atomic.AddInt32(&running, 1)
			for {
				p := atomic.LoadInt32(&peak)
				if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
					break
				}
			}
			<-start
			atomic.AddInt32(&running, -1)
		}(key)
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&running) < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(start)
	wg.Wait()
	if peak != 3 {
		t.Fatalf("different keys must not block each other, peak=%d", peak)
	}
}

func TestKeyedMutex_ReleasesEntries(t *testing.T) {
	var km keyedMutex
	for i := 0; i < 100; i++ {
		km.lock("k")()
	}
	km.mu.Lock()
	defer km.mu.Unlock()
	if len(km.locks) != 0 {
		t.Fatalf("idle keys must not accumulate, got %d", len(km.locks))
	}
}
