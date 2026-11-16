use std::sync::{Arc, Barrier};
use std::thread;
use std::time::Duration;

use chrono::Utc;
use serde_json::{json, Value};
use statsig_rust::networking::ResponseData;
use statsig_rust::sdk_event_emitter::SdkEventEmitter;
use statsig_rust::{SpecStore, SpecsSource, SpecsUpdate, StatsigErr, StatsigRuntime};

fn new_store(sdk_key: &str) -> SpecStore {
    SpecStore::new(
        sdk_key,
        sdk_key.to_string(),
        StatsigRuntime::get_runtime(),
        Arc::new(SdkEventEmitter::default()),
        None,
    )
}

fn fixture() -> Value {
    serde_json::from_str(include_str!("data/inventory.json")).unwrap()
}

fn apply(store: &SpecStore, config: Value, source: SpecsSource) -> Result<(), StatsigErr> {
    store.set_values(SpecsUpdate {
        data: ResponseData::from_bytes(serde_json::to_vec(&config).unwrap()),
        source,
        received_at: 42,
        source_api: None,
    })
}

#[test]
fn inventory_snapshot_exports_only_metadata_and_distinguishes_load_states() {
    let store = new_store("secret-inventory-contract");
    assert_eq!(
        serde_json::to_value(store.get_inventory_snapshot().unwrap()).unwrap(),
        json!({
            "loaded": false,
            "source": "uninitialized",
            "config_time": null,
            "checksum": null,
            "accepted_at": null,
            "inventory": null
        })
    );

    let before = Utc::now().timestamp_millis() as u64;
    apply(
        &store,
        fixture(),
        SpecsSource::Adapter("DataStore".to_string()),
    )
    .unwrap();
    let snapshot = store.get_inventory_snapshot().unwrap();
    let accepted_at = snapshot.accepted_at.unwrap();
    assert!((before..=Utc::now().timestamp_millis() as u64).contains(&accepted_at));
    assert_eq!(
        serde_json::to_value(snapshot).unwrap(),
        json!({
            "loaded": true,
            "source": "data_store",
            "config_time": 1000,
            "checksum": "inventory-a",
            "accepted_at": accepted_at,
            "inventory": {
                "feature_gates": {
                    "shared": ["web", "ios-☕"],
                    "removed_gate": ["web"],
                    "segment:audience": null
                },
                "dynamic_configs": {
                    "shared": [],
                    "experiment": ["web"],
                    "null_apps": null
                }
            }
        })
    );

    let mut empty = fixture();
    empty["time"] = json!(2000);
    empty["checksum"] = json!("inventory-empty");
    empty["feature_gates"] = json!({});
    empty["dynamic_configs"] = json!({});
    apply(&store, empty, SpecsSource::Bootstrap).unwrap();
    let snapshot = store.get_inventory_snapshot().unwrap();
    assert!(snapshot.loaded);
    let inventory = snapshot.inventory.unwrap();
    assert!(inventory.feature_gates.is_empty());
    assert!(inventory.dynamic_configs.is_empty());
    assert_eq!(snapshot.config_time, Some(2000));
    assert_eq!(snapshot.checksum.as_deref(), Some("inventory-empty"));
}

#[test]
fn inventory_snapshot_owns_its_data_and_follows_only_accepted_updates() {
    let store = new_store("secret-inventory-refresh");
    apply(&store, fixture(), SpecsSource::Network).unwrap();
    let mut original = store.get_inventory_snapshot().unwrap();
    let original_json = serde_json::to_value(&original).unwrap();
    original
        .inventory
        .as_mut()
        .unwrap()
        .feature_gates
        .get_mut("shared")
        .unwrap()
        .as_mut()
        .unwrap()[0] = "caller-mutation".to_string();
    assert_eq!(
        serde_json::to_value(store.get_inventory_snapshot().unwrap()).unwrap(),
        original_json
    );

    let mut next = fixture();
    next["time"] = json!(2000);
    next["checksum"] = json!("inventory-b");
    let removed = next["feature_gates"]
        .as_object_mut()
        .unwrap()
        .remove("removed_gate")
        .unwrap();
    next["feature_gates"]["added_gate"] = removed;
    next["feature_gates"]["shared"]["targetAppIDs"] = json!(["android"]);
    let removed = next["dynamic_configs"]
        .as_object_mut()
        .unwrap()
        .remove("experiment")
        .unwrap();
    next["dynamic_configs"]["added_experiment"] = removed;
    next["dynamic_configs"]["shared"]["targetAppIDs"] = json!(["web"]);
    apply(&store, next, SpecsSource::Bootstrap).unwrap();

    let accepted = serde_json::to_value(store.get_inventory_snapshot().unwrap()).unwrap();
    assert_eq!(accepted["config_time"], 2000);
    assert_eq!(accepted["checksum"], "inventory-b");
    assert_eq!(accepted["source"], "bootstrap");
    assert_eq!(
        accepted["inventory"],
        json!({
            "feature_gates": {
                "shared": ["android"],
                "added_gate": ["web"],
                "segment:audience": null
            },
            "dynamic_configs": {
                "shared": ["web"],
                "added_experiment": ["web"],
                "null_apps": null
            }
        })
    );
    assert_eq!(
        original.inventory.unwrap().dynamic_configs["shared"],
        Some(vec![])
    );
    assert_eq!(original.config_time, Some(1000));

    // Cross the millisecond clock resolution so a poll/read timestamp would differ.
    thread::sleep(Duration::from_millis(2));
    assert!(apply(&store, json!({"invalid": true}), SpecsSource::Network).is_err());
    apply(&store, json!({"has_updates": false}), SpecsSource::Network).unwrap();
    apply(&store, fixture(), SpecsSource::Network).unwrap();
    assert_eq!(
        serde_json::to_value(store.get_inventory_snapshot().unwrap()).unwrap(),
        accepted
    );
}

#[test]
fn inventory_snapshot_is_coherent_during_concurrent_refresh() {
    let store = Arc::new(new_store("secret-inventory-concurrent"));
    let mut initial = fixture();
    initial["time"] = json!(1001);
    initial["checksum"] = json!("inventory-1001");
    initial["feature_gates"]["shared"]["targetAppIDs"] = json!(["1001"]);
    initial["dynamic_configs"]["shared"]["targetAppIDs"] = json!(["1001"]);
    apply(&store, initial, SpecsSource::Network).unwrap();
    let start = Arc::new(Barrier::new(2));
    let writer_store = store.clone();
    let writer_start = start.clone();
    let writer = thread::spawn(move || {
        writer_start.wait();
        for version in 1002..=1100 {
            let mut config = fixture();
            config["time"] = json!(version);
            config["checksum"] = json!(format!("inventory-{version}"));
            config["feature_gates"]["shared"]["targetAppIDs"] = json!([version.to_string()]);
            config["dynamic_configs"]["shared"]["targetAppIDs"] = json!([version.to_string()]);
            apply(&writer_store, config, SpecsSource::Network).unwrap();
            thread::yield_now();
        }
    });

    start.wait();
    for _ in 0..1000 {
        let snapshot = store.get_inventory_snapshot().unwrap();
        let version = snapshot.config_time.unwrap();
        assert_eq!(snapshot.checksum, Some(format!("inventory-{version}")));
        let inventory = snapshot.inventory.unwrap();
        let expected = Some(vec![version.to_string()]);
        assert_eq!(inventory.feature_gates["shared"], expected);
        assert_eq!(inventory.dynamic_configs["shared"], expected);
        thread::yield_now();
    }
    writer.join().unwrap();
    assert_eq!(
        store.get_inventory_snapshot().unwrap().config_time,
        Some(1100)
    );
}
