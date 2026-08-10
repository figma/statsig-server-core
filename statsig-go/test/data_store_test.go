package test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	statsig_go "github.com/statsig-io/statsig-go-core"
)

type SetCall struct {
	key   string
	value string
	time  *uint64
}

type MockDataStore struct {
	initializeCalled                   bool
	shutdownCalled                     bool
	getCall                            *string
	setCall                            *SetCall
	shouldBeUsedForQueryingUpdatesCall *string
	getResult                          *string
}

func (m *MockDataStore) Initialize() {
	m.initializeCalled = true
}

func (m *MockDataStore) Shutdown() {
	m.shutdownCalled = true
}

func (m *MockDataStore) Get(key string) string {
	m.getCall = &key

	if m.getResult != nil {
		return *m.getResult
	}

	return "{\"result\": \"test\", \"time\": 1234567890}"
}

func (m *MockDataStore) Set(key string, value string, time *uint64) {
	m.setCall = &SetCall{
		key:   key,
		value: value,
		time:  time,
	}
}

func (m *MockDataStore) ShouldBeUsedForQueryingUpdates(key string) bool {
	m.shouldBeUsedForQueryingUpdatesCall = &key
	return false
}

func (m *MockDataStore) GetFunctions() statsig_go.DataStoreFunctions {
	return statsig_go.DataStoreFunctions{
		Initialize:                     m.Initialize,
		Shutdown:                       m.Shutdown,
		Get:                            m.Get,
		Set:                            m.Set,
		ShouldBeUsedForQueryingUpdates: m.ShouldBeUsedForQueryingUpdates,
	}
}

func TestDataStore(t *testing.T) {
	mockDataStore := &MockDataStore{}

	store := statsig_go.NewDataStore(mockDataStore.GetFunctions())

	store.INTERNAL_testDataStore("/v2/download_config_specs", "test")

	if !mockDataStore.initializeCalled {
		t.Error("Expected initialize to be called")
	}

	if *mockDataStore.getCall != "/v2/download_config_specs" {
		t.Error("Expected get to be called with /v2/download_config_specs")
	}

	setCall := mockDataStore.setCall
	if setCall == nil {
		t.Error("Expected set to be called")
	} else {
		if setCall.key != "/v2/download_config_specs" {
			t.Error("Expected set to be called with /v2/download_config_specs")
		}
		if setCall.value != "test" {
			t.Error("Expected set to be called with test")
		}
		if *setCall.time != 123 {
			t.Error("Expected set to be called with time")
		}
	}

	if *mockDataStore.shouldBeUsedForQueryingUpdatesCall != "/v2/download_config_specs" {
		t.Error("Expected shouldBeUsedForQueryingUpdates to be called with /v2/download_config_specs")
	}

	if !mockDataStore.shutdownCalled {
		t.Error("Expected shutdown to be called")
	}
}

type dataStoreTestResult struct {
	GetResult *struct {
		Result *string `json:"result"`
		Time   *uint64 `json:"time"`
	} `json:"get_result"`
}

func runDataStoreTest(t *testing.T, mock *MockDataStore, value string) dataStoreTestResult {
	t.Helper()

	store := statsig_go.NewDataStore(mock.GetFunctions())
	raw := store.INTERNAL_testDataStore("/v2/download_config_specs", value)

	var result dataStoreTestResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("Could not parse data store test result %q: %v", raw, err)
	}

	return result
}

// The core reads the get result with CStr::from_ptr, so an unterminated buffer
// takes whatever follows it in memory along with the payload.
func TestDataStoreGetResultIsReadExactly(t *testing.T) {
	payload := strings.Repeat("specs", 4096)
	response := fmt.Sprintf(`{"result": %q, "time": 1234567890}`, payload)

	result := runDataStoreTest(t, &MockDataStore{getResult: &response}, "test")

	if result.GetResult == nil || result.GetResult.Result == nil {
		t.Fatal("Expected the core to read back a get result")
	}

	if *result.GetResult.Result != payload {
		t.Errorf(
			"Get result was not read back exactly: got %d bytes, want %d",
			len(*result.GetResult.Result),
			len(payload),
		)
	}
}

// "" is the only way a DataStoreFunctions.Get can report a miss. It used to
// panic on &result[0]; the core should just see no data.
func TestDataStoreEmptyGetResultIsAMiss(t *testing.T) {
	empty := ""

	result := runDataStoreTest(t, &MockDataStore{getResult: &empty}, "test")

	if result.GetResult != nil {
		t.Errorf("Expected an empty get to be a miss, got %+v", *result.GetResult)
	}
}

// The set args are the full serialized specs. The core hands them over with
// CString::into_raw and never reclaims them, so a callback that does not free
// them leaks the whole payload on every write.
func TestDataStoreSetArgsAreNotLeaked(t *testing.T) {
	mock := &MockDataStore{}
	store := statsig_go.NewDataStore(mock.GetFunctions())
	value := strings.Repeat("a", leakTestPayloadBytes)

	growth := measureRssGrowth(t, func() {
		store.INTERNAL_testDataStore("/v2/download_config_specs", value)
	})

	if growth > leakTestThreshold {
		t.Errorf("Data store set leaked memory: %s", humanizeBytes(growth))
	}
}
