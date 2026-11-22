// SPDX-License-Identifier: MPL-2.0
#[tokio::main]
async fn main() -> std::process::ExitCode {
    match bugsnitch_server_migrate::http::run().await {
        Ok(()) => std::process::ExitCode::SUCCESS,
        Err(reason) => {
            eprintln!("{}", serde_json::json!({"level":"ERROR","message":reason}));
            std::process::ExitCode::FAILURE
        }
    }
}
