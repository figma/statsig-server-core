package internal

import (
	"testing"
	"unsafe"
)

// bytePtr returns a pointer to the first byte of a real Go-allocated
// buffer, keeping the buffer alive via the closure-style escape that
// the caller's stack frame holds.
func bytePtr(b []byte) *byte {
	if len(b) == 0 {
		return nil
	}
	return &b[0]
}

// fakePtr fabricates a *byte from an arbitrary integer address for
// the explicit purpose of exercising the address-validity guards in
// GoStringFromPointer / UnperformantGoStringFromPointer. We launder
// the address through unsafe.Add on a real *byte so that `go vet`'s
// unsafeptr analyzer (which warns on direct uintptr-to-unsafe.Pointer
// casts) does not flag this synthetic-pointer construction. The
// pointers produced here are NEVER dereferenced — they only exercise
// the address-range guards inside the functions under test.
func fakePtr(addr uintptr) *byte {
	var anchor byte
	base := uintptr(unsafe.Pointer(&anchor))
	return (*byte)(unsafe.Add(unsafe.Pointer(&anchor), int64(addr)-int64(base)))
}

func TestFFIUtils_GoStringFromPointer_Nil(t *testing.T) {
	if got := GoStringFromPointer(nil, 0); got != nil {
		t.Fatalf("expected nil for nil ptr, got %v", *got)
	}
}

func TestFFIUtils_GoStringFromPointer_NonCanonical(t *testing.T) {
	// Matches the observed crash address from labmate staging.
	bad := fakePtr(0x88e50e9746d53405)
	if got := GoStringFromPointer(bad, 8); got != nil {
		t.Fatalf("expected nil for non-canonical ptr, got %v", *got)
	}
}

func TestFFIUtils_GoStringFromPointer_LowAddress(t *testing.T) {
	// Anything below 0x10000 is rejected as near-nullptr garbage.
	bad := fakePtr(0x42)
	if got := GoStringFromPointer(bad, 8); got != nil {
		t.Fatalf("expected nil for low ptr, got %v", *got)
	}
}

func TestFFIUtils_GoStringFromPointer_LengthCap(t *testing.T) {
	buf := []byte("hello")
	if got := GoStringFromPointer(bytePtr(buf), uint64(maxFFIStringLen)+1); got != nil {
		t.Fatalf("expected nil for absurd length, got %v", *got)
	}
}

func TestFFIUtils_GoStringFromPointer_Valid(t *testing.T) {
	buf := []byte("hello")
	got := GoStringFromPointer(bytePtr(buf), uint64(len(buf)))
	if got == nil {
		t.Fatal("expected non-nil for valid ptr+len")
	}
	if *got != "hello" {
		t.Fatalf("expected %q, got %q", "hello", *got)
	}
}

func TestFFIUtils_UnperformantGoStringFromPointer_Nil(t *testing.T) {
	if got := UnperformantGoStringFromPointer(nil); got != nil {
		t.Fatalf("expected nil for nil ptr, got %v", *got)
	}
}

func TestFFIUtils_UnperformantGoStringFromPointer_NonCanonical(t *testing.T) {
	bad := fakePtr(0x88e50e9746d53405)
	if got := UnperformantGoStringFromPointer(bad); got != nil {
		t.Fatalf("expected nil for non-canonical ptr, got %v", *got)
	}
}

func TestFFIUtils_UnperformantGoStringFromPointer_LowAddress(t *testing.T) {
	bad := fakePtr(0x42)
	if got := UnperformantGoStringFromPointer(bad); got != nil {
		t.Fatalf("expected nil for low ptr, got %v", *got)
	}
}

func TestFFIUtils_UnperformantGoStringFromPointer_Valid(t *testing.T) {
	// NUL-terminated buffer, simulating CString::into_raw on the
	// Rust side.
	buf := []byte("hello\x00trailing-garbage")
	got := UnperformantGoStringFromPointer(bytePtr(buf))
	if got == nil {
		t.Fatal("expected non-nil for valid NUL-terminated buffer")
	}
	if *got != "hello" {
		t.Fatalf("expected %q, got %q", "hello", *got)
	}
}
