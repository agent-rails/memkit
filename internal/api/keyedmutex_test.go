package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustLock(t *testing.T, km *keyedMutex, key string) func() {
	t.Helper()
	unlock, err := km.lock(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return unlock
}

func TestKeyedMutex_SameKeyIsSerialized(t *testing.T) {
	var km keyedMutex
	var running, peak int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := mustLock(t, &km, "tenant\x00user")
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
			unlock := mustLock(t, &km, key)
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
		mustLock(t, &km, "k")()
	}
	km.mu.Lock()
	defer km.mu.Unlock()
	if len(km.locks) != 0 {
		t.Fatalf("idle keys must not accumulate, got %d", len(km.locks))
	}
}

func TestKeyedMutex_WaiterStopsWhenItsContextIsCancelled(t *testing.T) {
	var km keyedMutex
	holder := mustLock(t, &km, "k")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := km.lock(ctx, "k")
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled waiter must get an error")
		}
	case <-time.After(time.Second):
		t.Fatal("a cancelled waiter must stop waiting promptly")
	}
	holder()
	km.mu.Lock()
	leaked := len(km.locks)
	km.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("a cancelled waiter must not leak its entry, got %d", leaked)
	}
	next, err := km.lock(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	next()
}
