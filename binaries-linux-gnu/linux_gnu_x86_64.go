//go:build linux && amd64

package go_server_core_binaries_linux_gnu

import _ "embed"

//go:embed linux_gnu_x86_64.so
var binaryData []byte

// GetBinaryData returns the embedded libstatsig_ffi.so bytes.
// Symbol parity with upstream — the wrapper's internal/use_linux.go reads this.
func GetBinaryData() []byte { return binaryData }

// GetSignatureData returns nil. Upstream wrapper fetches but does not verify
// the .sig (statsig_ffi.go:loadLibrary at v0.19.3).
func GetSignatureData() []byte { return nil }
