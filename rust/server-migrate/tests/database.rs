// SPDX-License-Identifier: MPL-2.0
use std::path::Path;
use std::process::Command;

#[test]
#[ignore = "requires psql, python3 and an explicitly selected disposable local PostgreSQL database"]
fn migrates_and_preserves_the_postgres_ledger() {
    let harness = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/database.py");
    let status = Command::new("python3")
        .arg(harness)
        .arg(env!("CARGO_BIN_EXE_bugsnitch-server-migrate"))
        .status()
        .expect("start the PostgreSQL fixture with python3");
    assert!(status.success(), "PostgreSQL migration fixture failed");
}
