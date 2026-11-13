package statsig_go_core

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"

	"github.com/ebitengine/purego"
)

// Inventory contains every raw name in each namespace, including segments and
// experiments. A present key with a nil slice has no target-app metadata; a
// non-nil, empty slice represents an explicitly empty app list.
type Inventory struct {
	FeatureGates   map[string][]string `json:"feature_gates"`
	DynamicConfigs map[string][]string `json:"dynamic_configs"`
}

// InventorySnapshot is an owned view of one accepted SDK configuration. Reading
// it does not evaluate flags, create users, or enqueue exposure events.
type InventorySnapshot struct {
	Loaded     bool            `json:"loaded"`
	Source     InventorySource `json:"source"`
	ConfigTime *uint64         `json:"config_time"` // Provider time in Unix milliseconds.
	Checksum   *string         `json:"checksum"`
	AcceptedAt *uint64         `json:"accepted_at"` // Acceptance time, not the last successful poll.
	Inventory  *Inventory      `json:"inventory"`   // Nil until a configuration has been accepted.
}

type InventorySource string

const (
	InventorySourceUninitialized InventorySource = "uninitialized"
	InventorySourceNoValues      InventorySource = "no_values"
	InventorySourceError         InventorySource = "error"
	InventorySourceLoading       InventorySource = "loading"
	InventorySourceBootstrap     InventorySource = "bootstrap"
	InventorySourceDataStore     InventorySource = "data_store"
	InventorySourceAdapter       InventorySource = "adapter"
	InventorySourceNetwork       InventorySource = "network"
)

// ErrInventorySnapshotUnsupported indicates that the loaded native library
// predates the inventory API. Upgrade the Go and native modules together.
var ErrInventorySnapshotUnsupported = errors.New("native Statsig library does not support inventory snapshots")

func (s *Statsig) GetInventorySnapshot() (*InventorySnapshot, error) {
	ffi := GetFFI()
	// Some platform binary modules are released separately. Resolve this additive
	// API lazily so an older native library can still serve existing SDK methods.
	ffi.inventoryOnce.Do(func() {
		symbol, err := purego.Dlsym(ffi.lib, "statsig_get_inventory_snapshot_with_inout_len")
		if err != nil {
			ffi.inventoryError = fmt.Errorf("%w: %v", ErrInventorySnapshotUnsupported, err)
			return
		}
		purego.RegisterFunc(&ffi.statsig_get_inventory_snapshot_with_inout_len, symbol)
	})
	if ffi.inventoryError != nil {
		return nil, ffi.inventoryError
	}

	snapshotJSON := UseRustString(func() (*byte, uint64) {
		var length uint64
		ptr := ffi.statsig_get_inventory_snapshot_with_inout_len(s.ref.Load(), &length)
		return ptr, length
	})
	runtime.KeepAlive(s)
	if snapshotJSON == nil {
		return nil, errors.New("reading inventory snapshot from native Statsig library")
	}

	var snapshot InventorySnapshot
	if err := json.Unmarshal([]byte(*snapshotJSON), &snapshot); err != nil {
		return nil, fmt.Errorf("decoding inventory snapshot: %w", err)
	}
	return &snapshot, nil
}
