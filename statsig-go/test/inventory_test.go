package test

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	statsig "github.com/statsig-io/statsig-go-core"
)

func newInventorySDK(t *testing.T, get func(string) string) (*statsig.Statsig, *MockScrapi) {
	t.Helper()
	scrapi := NewMockScrapi()
	t.Cleanup(scrapi.Close)
	scrapi.Stub("POST", "/v1/log_event", JSON(http.StatusOK, map[string]bool{"success": true}))
	store := statsig.NewDataStore(statsig.DataStoreFunctions{
		Initialize:                     func() {},
		Shutdown:                       func() {},
		Get:                            get,
		Set:                            func(string, string, *uint64) {},
		ShouldBeUsedForQueryingUpdates: func(string) bool { return true },
	})
	opts, err := statsig.NewOptionsBuilder().
		WithDataStore(store).
		WithSpecsUrl(scrapi.URL() + "/v2/download_config_specs").
		WithLogEventUrl(scrapi.URL() + "/v1/log_event").
		WithSpecsSyncIntervalMs(1000).
		WithEventLoggingFlushIntervalMs(60000).
		WithDisableCountryLookup(true).
		WithDisableUserAgentParsing(true).
		WithOutputLogLevel("none").
		Build()
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := statsig.NewStatsigWithOptions("secret-123", opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sdk.Shutdown()
		runtime.KeepAlive(store)
	})
	return sdk, scrapi
}

func getInventory(t *testing.T, sdk *statsig.Statsig) *statsig.InventorySnapshot {
	t.Helper()
	snapshot, err := sdk.GetInventorySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil {
		t.Fatal("inventory read returned neither a snapshot nor an error")
	}
	return snapshot
}

func inventoryEnvelope(t *testing.T, config map[string]any) string {
	t.Helper()
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(map[string]any{"result": string(body)})
	if err != nil {
		t.Fatal(err)
	}
	return string(envelope)
}

func TestInventorySnapshotDataStoreRefresh(t *testing.T) {
	data, err := os.ReadFile("../../statsig-rust/tests/data/inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	var response atomic.Value
	response.Store(inventoryEnvelope(t, config))
	sdk, scrapi := newInventorySDK(t, func(string) string { return response.Load().(string) })

	// Exercise concurrent reads, including lazy native-symbol resolution.
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			snapshot, err := sdk.GetInventorySnapshot()
			if err != nil {
				t.Error(err)
				return
			}
			if !reflect.DeepEqual(snapshot, &statsig.InventorySnapshot{Source: statsig.InventorySourceUninitialized}) {
				t.Errorf("expected an unloaded snapshot, got %+v", snapshot)
			}
		}()
	}
	readers.Wait()

	before := uint64(time.Now().UnixMilli())
	sdk.Initialize()
	snapshot := getInventory(t, sdk)
	want := &statsig.Inventory{
		FeatureGates: map[string][]string{
			"shared":           {"web", "ios-☕"},
			"removed_gate":     {"web"},
			"segment:audience": nil,
		},
		DynamicConfigs: map[string][]string{
			"shared":     {},
			"experiment": {"web"},
			"null_apps":  nil,
		},
	}
	if !snapshot.Loaded || snapshot.Source != statsig.InventorySourceDataStore || !reflect.DeepEqual(snapshot.Inventory, want) {
		t.Fatalf("snapshot = %+v, inventory = %+v; want %+v", snapshot, snapshot.Inventory, want)
	}
	if snapshot.ConfigTime == nil || *snapshot.ConfigTime != 1000 || snapshot.Checksum == nil || *snapshot.Checksum != "inventory-a" {
		t.Fatalf("incorrect configuration version: %+v", snapshot)
	}
	if snapshot.AcceptedAt == nil || *snapshot.AcceptedAt < before || *snapshot.AcceptedAt > uint64(time.Now().UnixMilli()) {
		t.Fatalf("incorrect acceptance time: %+v", snapshot)
	}

	snapshot.Inventory.FeatureGates["shared"][0] = "caller-mutation"
	*snapshot.ConfigTime = 999
	if fresh := getInventory(t, sdk); *fresh.ConfigTime != 1000 || !reflect.DeepEqual(fresh.Inventory, want) {
		t.Fatalf("caller mutation changed SDK inventory: %+v", fresh)
	}

	config["time"] = 2000
	config["checksum"] = "inventory-b"
	gates := config["feature_gates"].(map[string]any)
	gates["added_gate"] = gates["removed_gate"]
	delete(gates, "removed_gate")
	gates["shared"].(map[string]any)["targetAppIDs"] = []string{"android"}
	gates["shared"].(map[string]any)["defaultValue"] = false
	configs := config["dynamic_configs"].(map[string]any)
	configs["added_experiment"] = configs["experiment"]
	delete(configs, "experiment")
	configs["shared"].(map[string]any)["targetAppIDs"] = []string{"web"}
	response.Store(inventoryEnvelope(t, config))

	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot = getInventory(t, sdk)
		if snapshot.ConfigTime != nil && *snapshot.ConfigTime == 2000 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("SDK did not accept updated inventory: %+v", snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}
	delete(want.FeatureGates, "removed_gate")
	want.FeatureGates["added_gate"] = []string{"web"}
	want.FeatureGates["shared"] = []string{"android"}
	delete(want.DynamicConfigs, "experiment")
	want.DynamicConfigs["added_experiment"] = []string{"web"}
	want.DynamicConfigs["shared"] = []string{"web"}
	if !reflect.DeepEqual(snapshot.Inventory, want) || snapshot.Checksum == nil || *snapshot.Checksum != "inventory-b" {
		t.Fatalf("updated snapshot = %+v, inventory = %+v; want %+v", snapshot, snapshot.Inventory, want)
	}

	sdk.FlushEvents()
	if events := scrapi.Events(); len(events) != 0 {
		t.Fatalf("inventory reads logged events: %+v", events)
	}
	// An evaluation must reach the same collector, proving it observes exposures.
	user, err := sdk.NewUserBuilderWithUserID("inventory-user").Build()
	if err != nil {
		t.Fatal(err)
	}
	gate := sdk.GetFeatureGate(user, "shared")
	if gate.Value || gate.EvaluationDetails.LCUT != *snapshot.ConfigTime || gate.EvaluationDetails.ReceivedAt != *snapshot.AcceptedAt {
		t.Fatalf("evaluation did not use the accepted inventory configuration: %+v", gate)
	}
	sdk.FlushEvents()
	if events := scrapi.Events(); len(events) != 1 || events[0]["eventName"] != "statsig::gate_exposure" {
		t.Fatalf("evaluation control did not produce its exposure: %+v", events)
	}

	config["time"] = 3000
	config["checksum"] = "inventory-empty"
	config["feature_gates"] = map[string]any{}
	config["dynamic_configs"] = map[string]any{}
	empty := inventoryEnvelope(t, config)
	other, _ := newInventorySDK(t, func(string) string { return empty })
	other.Initialize()
	loadedEmpty := getInventory(t, other)
	if !loadedEmpty.Loaded || !reflect.DeepEqual(loadedEmpty.Inventory, &statsig.Inventory{
		FeatureGates:   map[string][]string{},
		DynamicConfigs: map[string][]string{},
	}) {
		t.Fatalf("expected a loaded empty snapshot: %+v", loadedEmpty)
	}
	if fresh := getInventory(t, sdk); *fresh.ConfigTime != 2000 || !reflect.DeepEqual(fresh.Inventory, want) {
		t.Fatalf("another SDK instance changed the original inventory: %+v", fresh)
	}
}

func TestInventorySnapshotCDNFallback(t *testing.T) {
	for _, format := range []string{"json", "protobuf"} {
		t.Run(format, func(t *testing.T) {
			var reads atomic.Int32
			sdk, scrapi := newInventorySDK(t, func(string) string {
				reads.Add(1)
				return `{"result":null}`
			})
			filename := "demo_proj_dcs.json"
			header := http.Header{"Content-Type": {"application/json"}}
			if format == "protobuf" {
				filename = "demo_proj_dcs.pb.br"
				header.Set("Content-Type", "application/octet-stream")
				header.Set("Content-Encoding", "statsig-br")
			}
			data, err := os.ReadFile("../../statsig-rust/tests/data/" + filename)
			if err != nil {
				t.Fatal(err)
			}
			scrapi.Stub("GET", "/v2/download_config_specs/secret-123.json", Bytes(http.StatusOK, data, header))
			sdk.Initialize()
			snapshot := getInventory(t, sdk)
			if reads.Load() == 0 || !snapshot.Loaded || snapshot.Source != statsig.InventorySourceNetwork || snapshot.Inventory == nil {
				t.Fatalf("datastore miss did not expose accepted CDN configuration: %+v", snapshot)
			}
			if snapshot.ConfigTime == nil || *snapshot.ConfigTime != 1767920036437 || snapshot.Checksum == nil || *snapshot.Checksum != "6221227889497997000" || snapshot.AcceptedAt == nil {
				t.Fatalf("incorrect CDN configuration metadata: %+v", snapshot)
			}
			if len(snapshot.Inventory.FeatureGates) != 9 || len(snapshot.Inventory.DynamicConfigs) != 8 {
				t.Fatalf("CDN inventory lost raw names: %+v", snapshot.Inventory)
			}
			if _, ok := snapshot.Inventory.FeatureGates["segment:inline_targeting:brock_experiment"]; !ok {
				t.Fatal("CDN inventory omitted the raw segment name")
			}
			if apps := snapshot.Inventory.DynamicConfigs["brock_experiment"]; !reflect.DeepEqual(apps, []string{"5YNyv8RY589KRsHZ42NjUj"}) {
				t.Fatalf("CDN inventory lost experiment app IDs: %+v", apps)
			}
			user, err := sdk.NewUserBuilderWithUserID("inventory-fallback-user").Build()
			if err != nil {
				t.Fatal(err)
			}
			gate := sdk.GetFeatureGateWithOptions(user, "test_public", &statsig.FeatureGateEvaluationOptions{DisableExposureLogging: true})
			if gate.EvaluationDetails.LCUT != *snapshot.ConfigTime || gate.EvaluationDetails.ReceivedAt != *snapshot.AcceptedAt {
				t.Fatalf("evaluation and fallback inventory used different configurations: %+v", gate)
			}
		})
	}
}

func TestInventorySnapshotInvalidInstance(t *testing.T) {
	var sdk statsig.Statsig
	snapshot, err := sdk.GetInventorySnapshot()
	if err == nil || snapshot != nil {
		t.Fatalf("invalid native instance returned snapshot=%+v, error=%v", snapshot, err)
	}
}
