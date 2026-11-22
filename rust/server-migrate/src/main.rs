// SPDX-License-Identifier: MPL-2.0
mod config;
mod migrations;

use std::{env, process::ExitCode, time::Duration};

fn main() -> ExitCode {
    if env::args_os().len() != 1 {
        eprintln!("Usage: bugsnitch-server-migrate (set DATABASE_URL)");
        return ExitCode::FAILURE;
    }
    let timeout = match env::var("BUGSNITCH_MIGRATION_TIMEOUT_MS") {
        Ok(ms) => match ms.parse::<u64>() {
            Ok(ms) if (1..=i32::MAX as u64).contains(&ms) => ms,
            _ => {
                eprintln!("Invalid migration timeout");
                return ExitCode::FAILURE;
            }
        },
        Err(_) => 30_000,
    };
    // libpq connection setup is also covered, including stalled remote hosts.
    std::thread::spawn(move || {
        std::thread::sleep(Duration::from_millis(timeout));
        eprintln!("Database migration deadline exceeded");
        std::process::exit(1);
    });
    match migrations::run(timeout) {
        Ok(()) => ExitCode::SUCCESS,
        Err(message) => {
            eprintln!("{message}");
            ExitCode::FAILURE
        }
    }
}
