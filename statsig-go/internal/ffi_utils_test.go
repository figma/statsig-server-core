package internal

import (
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

// Big enough that the runtime gives each buffer its own block, so a finalizer
// on it tracks that buffer and nothing else.
func testPayload(seed string) string {
	return strings.Repeat(seed, 1024)
}

func readRetained(ptr *byte, length int) string {
	return string(unsafe.Slice(ptr, length))
}

// retainWatched retains payload and reports through freed when the buffer
// behind the returned pointer becomes unreachable. The pointer is deliberately
// not returned: the point is to test what keeps the buffer alive once the
// callback that produced it has no reference left, so only the keeper does.
func retainWatched(k *ResultKeeper, payload string, freed *atomic.Bool) {
	ptr := k.Retain(payload)
	runtime.SetFinalizer(ptr, func(*byte) {
		freed.Store(true)
	})
}

func collectGarbage() {
	for range 5 {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForFreed(freed *atomic.Bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if freed.Load() {
			return true
		}
		collectGarbage()
	}

	return freed.Load()
}

func TestResultKeeperCopiesAndNulTerminates(t *testing.T) {
	k := NewResultKeeper(1)
	payload := testPayload("a")

	ptr := k.Retain(payload)

	if got := readRetained(ptr, len(payload)); got != payload {
		t.Errorf("retained content does not match: got %d bytes, want %d", len(got), len(payload))
	}

	if terminator := readRetained(ptr, len(payload)+1)[len(payload)]; terminator != 0 {
		t.Errorf("retained buffer is not NUL-terminated: trailing byte is %q", terminator)
	}
}

func TestResultKeeperRetainsEmptyResult(t *testing.T) {
	k := NewResultKeeper(1)

	ptr := k.RetainBytes(nil)

	if ptr == nil {
		t.Fatal("expected a pointer to a lone terminator, got nil")
	}

	if *ptr != 0 {
		t.Errorf("expected an empty NUL-terminated buffer, got %q", *ptr)
	}
}

// The core reads the pointer with CStr::from_ptr after the callback has
// returned, so the buffer has to survive a GC with no Go reference to it other
// than the keeper's.
func TestResultKeeperHoldsResultAfterCallbackReturns(t *testing.T) {
	k := NewResultKeeper(1)
	var freed atomic.Bool

	retainWatched(k, testPayload("b"), &freed)
	collectGarbage()

	if freed.Load() {
		t.Error("retained buffer was collected while the keeper still held it")
	}

	// Without this the keeper is dead after its last use and the buffer goes
	// with it - which is the same reason a slice local to the callback cannot
	// be handed to the core.
	runtime.KeepAlive(k)
}

func TestResultKeeperReleasesDisplacedResults(t *testing.T) {
	const slots = 4
	k := NewResultKeeper(slots)

	var freed atomic.Bool
	retainWatched(k, testPayload("c"), &freed)

	for range slots - 1 {
		k.Retain(testPayload("d"))
	}

	collectGarbage()
	if freed.Load() {
		t.Fatal("buffer was released while still within the keeper's slots")
	}

	k.Retain(testPayload("e"))

	if !waitForFreed(&freed) {
		t.Error("displaced buffer was never released - the keeper retains without bound")
	}

	runtime.KeepAlive(k)
}

func TestResultKeeperConcurrentRetain(t *testing.T) {
	const (
		goroutines = 8
		perRoutine = 200
	)

	k := NewResultKeeper(goroutines)

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()

			payload := testPayload(string(rune('A' + i)))
			for range perRoutine {
				ptr := k.Retain(payload)
				if got := readRetained(ptr, len(payload)); got != payload {
					t.Errorf("goroutine %d read back a buffer it did not retain", i)
					return
				}
			}
		}()
	}
	wg.Wait()
}
