package internal

import (
	"sync"
	"unsafe"
)

// ResultKeeper holds on to the buffers a callback hands back to the core.
//
// The core reads a returned pointer with CStr::from_ptr *after* the callback
// has returned, so a slice allocated inside the callback is unreachable from
// Go - and collectable - by the time it is read. Each buffer is kept alive
// until a later result displaces it, which is what statsig-dotnet does with
// its single pinned get-result handle.
//
// slots is how many results stay live at once: one is enough for a caller the
// core only invokes serially, more for one it can invoke concurrently.
type ResultKeeper struct {
	mu    sync.Mutex
	slots [][]byte
	next  int
}

func NewResultKeeper(slots int) *ResultKeeper {
	return &ResultKeeper{slots: make([][]byte, slots)}
}

func (k *ResultKeeper) Retain(s string) *byte {
	buf := make([]byte, len(s)+1) // zero-filled, so already NUL-terminated
	copy(buf, s)
	return k.keep(buf)
}

func (k *ResultKeeper) RetainBytes(b []byte) *byte {
	buf := make([]byte, len(b)+1)
	copy(buf, b)
	return k.keep(buf)
}

func (k *ResultKeeper) keep(buf []byte) *byte {
	k.mu.Lock()
	k.slots[k.next] = buf
	k.next = (k.next + 1) % len(k.slots)
	k.mu.Unlock()

	return &buf[0]
}

func GoStringFromPointer(inputPtr *byte, inputLength uint64) *string {
	if inputPtr == nil {
		return nil
	}

	s := string(unsafe.Slice(inputPtr, inputLength))
	return &s
}

func UnperformantGoStringFromPointer(inputPtr *byte) *string {
	if inputPtr == nil {
		return nil
	}

	var n uintptr
	for {
		if *(*byte)(unsafe.Add(unsafe.Pointer(inputPtr), n)) == 0 {
			break
		}
		n++
	}

	s := string(unsafe.Slice(inputPtr, n))
	return &s
}
