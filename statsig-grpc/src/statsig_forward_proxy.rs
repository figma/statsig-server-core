// Tonic generates Status-returning methods and boxed async-trait futures.
#[allow(clippy::result_large_err, clippy::double_must_use)]
pub mod api {
    tonic::include_proto!("statsig_forward_proxy");
}

pub use api::*;
