//! Regression test for unbounded growth in
//! `ExposureSampling::should_dedupe_exposure`.
//!
//! Ported from upstream PR #47 on `statsig-io/statsig-server-core`
//! (reproducer by user `h-2`). Each iteration uses a distinct
//! `user_id`, which previously inserted into an unbounded
//! `HashSet<ExposureSamplingKey>` and grew RSS by ~6.7 KB per call.
//!
//! With the bounded LRU fix, RSS growth should stay well under the
//! 50 MB threshold below over 10k iterations on a minimal user.
//!
//! The test deliberately sets a SMALL `exposure_dedupe_max_keys`
//! (1_000) — much less than the iteration count — so the LRU's
//! insert-time eviction is exercised. Without the fix (unbounded
//! `HashSet`) the cache would hold all 10k unique keys and balloon
//! RSS past the threshold; with the fix it stays at the cap.
//!
//! Marked `#[ignore]` because RSS-based assertions are platform-
//! sensitive (Linux `/proc/self/status` only) and the workload is
//! heavyweight relative to a normal `cargo test` run.
//!
//! Run manually with:
//!
//!     cargo test --release \
//!         --test memory_leak_per_request_users_tests -- --ignored

mod utils;

use crate::utils::mock_event_logging_adapter::MockEventLoggingAdapter;
use crate::utils::mock_specs_adapter::MockSpecsAdapter;
use statsig_rust::{
    ClientInitResponseOptions, Statsig, StatsigOptions, StatsigUser,
};
use std::sync::Arc;

const ITERATIONS: usize = 10_000;
const DEDUPE_CAP: usize = 1_000;                     // « ITERATIONS, forces eviction
const MAX_RSS_GROWTH_BYTES: i64 = 50 * 1024 * 1024;  // 50 MB

const GATE_NAME: &str = "test_small_pass_gate";
const CONFIG_NAME: &str = "test_experiment_no_targeting";
const EXPERIMENT_NAME: &str = "an_experiment1";
const LAYER_NAME: &str = "test_layer_with_no_exp";

/// Read current resident set size in bytes from `/proc/self/status`.
/// Returns `None` on non-Linux platforms or if the field is missing.
#[cfg(target_os = "linux")]
fn read_rss_bytes() -> Option<i64> {
    let contents = std::fs::read_to_string("/proc/self/status").ok()?;
    for line in contents.lines() {
        if let Some(rest) = line.strip_prefix("VmRSS:") {
            // Format: `VmRSS:\t   12345 kB`
            let kb: i64 = rest.split_whitespace().next()?.parse().ok()?;
            return Some(kb * 1024);
        }
    }
    None
}

#[cfg(not(target_os = "linux"))]
fn read_rss_bytes() -> Option<i64> {
    None
}

#[tokio::test]
#[ignore]
async fn test_memory_leak_per_request_users_bounded() {
    let specs_adapter = Arc::new(MockSpecsAdapter::with_data(
        "tests/data/eval_proj_dcs.json",
    ));
    let logging_adapter = Arc::new(MockEventLoggingAdapter::new());

    let statsig = Statsig::new(
        "secret-shhh",
        Some(Arc::new(StatsigOptions {
            specs_adapter: Some(specs_adapter.clone()),
            event_logging_adapter: Some(logging_adapter.clone()),
            exposure_dedupe_max_keys: Some(DEDUPE_CAP),
            ..StatsigOptions::new()
        })),
    );
    statsig.initialize().await.unwrap();

    // Touch the cache once before measuring, so any one-time allocation
    // (e.g. lazy globals, hashtable bucket warm-up) is excluded from the
    // delta.
    {
        let warmup_user = StatsigUser::with_user_id("warmup");
        let _ = statsig.check_gate(&warmup_user, GATE_NAME);
    }

    let initial_rss = read_rss_bytes()
        .expect("RSS reading is only supported on Linux; run with --target-os=linux");

    for i in 0..ITERATIONS {
        // Distinct user_id per iteration — the adversarial pattern that
        // previously grew the dedupe set unboundedly.
        let user = StatsigUser::with_user_id(format!("user-{i}"));

        let _ = statsig.check_gate(&user, GATE_NAME);
        let _ = statsig.get_dynamic_config(&user, CONFIG_NAME);
        let _ = statsig.get_experiment(&user, EXPERIMENT_NAME);
        let _ = statsig.get_layer(&user, LAYER_NAME);
        let _ = statsig.get_client_init_response_with_options(
            &user,
            &ClientInitResponseOptions::default(),
        );
    }

    let final_rss = read_rss_bytes().expect("RSS read after loop");

    let delta = final_rss - initial_rss;
    println!(
        "RSS: initial = {:.2} MB, final = {:.2} MB, delta = {:+.2} MB",
        initial_rss as f64 / 1024.0 / 1024.0,
        final_rss as f64 / 1024.0 / 1024.0,
        delta as f64 / 1024.0 / 1024.0,
    );

    let _ = statsig.shutdown().await;

    assert!(
        delta < MAX_RSS_GROWTH_BYTES,
        "RSS grew by {} bytes (> {} bytes); exposure dedupe cache may be unbounded again",
        delta,
        MAX_RSS_GROWTH_BYTES,
    );
}
