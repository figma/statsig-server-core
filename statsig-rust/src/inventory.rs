use std::collections::HashMap;

use serde::Serialize;

use crate::SpecsSource;

/// Names and target-app metadata from one accepted configuration. This does not
/// contain targeting rules, evaluated values, or user information.
#[derive(Debug, Clone, Serialize)]
pub struct Inventory {
    pub feature_gates: HashMap<String, Option<Vec<String>>>,
    pub dynamic_configs: HashMap<String, Option<Vec<String>>>,
}

/// An owned, atomic view of the configuration used for evaluation.
#[derive(Debug, Clone, Serialize)]
pub struct InventorySnapshot {
    pub loaded: bool,
    pub source: InventorySource,
    /// Provider configuration time in Unix milliseconds, when loaded.
    pub config_time: Option<u64>,
    pub checksum: Option<String>,
    /// Unix milliseconds when the SDK accepted this configuration, not the last poll.
    pub accepted_at: Option<u64>,
    /// Absent before the first accepted configuration; present even when empty.
    pub inventory: Option<Inventory>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum InventorySource {
    Uninitialized,
    NoValues,
    Error,
    Loading,
    Bootstrap,
    DataStore,
    Adapter,
    Network,
}

impl From<&SpecsSource> for InventorySource {
    fn from(source: &SpecsSource) -> Self {
        match source {
            SpecsSource::Uninitialized => Self::Uninitialized,
            SpecsSource::NoValues => Self::NoValues,
            SpecsSource::Error => Self::Error,
            SpecsSource::Loading => Self::Loading,
            SpecsSource::Bootstrap => Self::Bootstrap,
            SpecsSource::Adapter(name) if name == "DataStore" => Self::DataStore,
            SpecsSource::Adapter(_) => Self::Adapter,
            SpecsSource::Network => Self::Network,
        }
    }
}
