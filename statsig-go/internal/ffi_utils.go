package internal

import "unsafe"

const maxFFIStringLen = 16 << 20 // 16 MiB — generous upper bound

// isCanonicalUserAddr returns true if addr is a plausible x86-64
// userspace virtual address. On x86-64 the architecture requires
// bits 47..63 to be a sign-extension of bit 47; for userspace this
// means addr < 1<<47. We also reject the bottom 64 KiB to catch
// near-nullptr garbage.
func isCanonicalUserAddr(addr uintptr) bool {
	return addr >= 0x10000 && addr < 1<<47
}

func GoStringFromPointer(inputPtr *byte, inputLength uint64) *string {
	if inputPtr == nil {
		return nil
	}
	addr := uintptr(unsafe.Pointer(inputPtr))
	// Reject non-canonical x86-64 user virtual addresses
	// (bits 47..63 must be sign-extension of bit 47; for userspace
	// that means addr < 1<<47).
	if !isCanonicalUserAddr(addr) {
		return nil
	}
	if inputLength > maxFFIStringLen {
		return nil
	}

	s := string(unsafe.Slice(inputPtr, inputLength))
	return &s
}

func UnperformantGoStringFromPointer(inputPtr *byte) *string {
	if inputPtr == nil {
		return nil
	}
	// Defense in depth: reject non-canonical pointers before walking
	// the buffer. Without this guard, Fix A would bypass Fix B and
	// the NUL-scan loop would itself dereference garbage.
	if !isCanonicalUserAddr(uintptr(unsafe.Pointer(inputPtr))) {
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
