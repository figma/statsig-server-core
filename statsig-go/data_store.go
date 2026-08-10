package statsig_go_core

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"github.com/statsig-io/statsig-go-core/internal"
)

type DataStoreFunctions struct {
	Initialize                     func()
	Shutdown                       func()
	Get                            func(key string) string
	Set                            func(key string, value string, time *uint64)
	ShouldBeUsedForQueryingUpdates func(key string) bool
}

type DataStore struct {
	functions DataStoreFunctions
	ref       uint64

	// A get result is the cached specs - ~18MB for a large project - so hold
	// exactly one, the way statsig-dotnet does. StatsigDataStoreSpecsAdapter
	// is the only caller and it reads serially: start(), then one background
	// sync tick at a time.
	getResults *internal.ResultKeeper
}

func NewDataStore(functions DataStoreFunctions) *DataStore {
	store := &DataStore{
		functions:  functions,
		ref:        0,
		getResults: internal.NewResultKeeper(1),
	}

	store.ref = GetFFI().data_store_create(
		store.functions.Initialize,
		store.functions.Shutdown,
		// Get
		func(argPtr *byte, argLength uint64) *byte {
			// The core hands these args over via CString::into_raw, so the
			// callback owns them. statsig-dotnet frees them the same way in
			// its finally blocks; Go was the binding that never did.
			defer GetFFI().free_string(argPtr)

			keyStr := internal.GoStringFromPointer(argPtr, argLength)
			if keyStr == nil {
				return nil
			}

			result := store.functions.Get(*keyStr)
			if result == "" {
				// "" is the only way the adapter can say "no data". Returning
				// a pointer to it just hands the core an empty string to fail
				// deserializing; nil is the miss the core already handles.
				return nil
			}

			// The core reads this with CStr::from_ptr once the callback has
			// returned, so it has to be NUL-terminated and it has to still be
			// reachable from Go.
			return store.getResults.Retain(result)
		},
		// Set
		func(argPtr *byte, argLength uint64) {
			// args_json here is the full serialized specs, ~18MB for a large
			// project, leaked once per write until this freed it.
			defer GetFFI().free_string(argPtr)

			data, err := tryMarshalDataStoreSetArgs(argPtr, argLength)
			if err != nil {
				fmt.Println("Error marshalling DataStore 'set' args", err)
				return
			}

			keyStr := data.Key
			valueStr := data.Value
			time := data.Time
			store.functions.Set(keyStr, valueStr, time)
		},
		// ShouldBeUsedForQueryingUpdates
		func(argPtr *byte, argLength uint64) bool {
			defer GetFFI().free_string(argPtr)

			keyStr := internal.GoStringFromPointer(argPtr, argLength)
			if keyStr == nil {
				return false
			}

			return store.functions.ShouldBeUsedForQueryingUpdates(*keyStr)
		},
	)

	runtime.SetFinalizer(store, func(obj *DataStore) {
		GetFFI().data_store_release(obj.ref)
	})

	return store
}

func (d *DataStore) INTERNAL_testDataStore(path string, value string) string {
	return GetFFI().__internal__test_data_store(d.ref, path, value)
}

type dataStoreSetArgs struct {
	Key   string  `json:"key"`
	Value string  `json:"value"`
	Time  *uint64 `json:"time"`
}

func tryMarshalDataStoreSetArgs(inputPtr *byte, inputLength uint64) (*dataStoreSetArgs, error) {
	if inputPtr == nil {
		return nil, errors.New("nil data store set args")
	}

	// Decode straight out of the C buffer. json.Unmarshal neither retains nor
	// mutates its input and the decoded fields are Go-owned copies, so this
	// skips two full-size copies of args_json - ~18MB each on our specs.
	var args dataStoreSetArgs
	if err := json.Unmarshal(unsafe.Slice(inputPtr, inputLength), &args); err != nil {
		return nil, err
	}

	return &args, nil
}
