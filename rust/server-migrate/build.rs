// SPDX-License-Identifier: MPL-2.0
use std::{env, fs, path::PathBuf, process::Command};

fn main() {
    println!("cargo:rerun-if-env-changed=PG_CONFIG");
    println!("cargo:rerun-if-env-changed=PGBIN");
    let pg_config = env::var_os("PG_CONFIG")
        .map(PathBuf::from)
        .or_else(|| env::var_os("PGBIN").map(|dir| PathBuf::from(dir).join("pg_config")))
        .unwrap_or_else(|| "pg_config".into());
    let libdir = Command::new(pg_config)
        .arg("--libdir")
        .output()
        .expect("install PostgreSQL development tools (pg_config)");
    assert!(libdir.status.success(), "pg_config --libdir failed");
    println!(
        "cargo:rustc-link-search=native={}",
        std::str::from_utf8(&libdir.stdout)
            .expect("invalid PostgreSQL library directory")
            .trim()
    );

    let source =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../internal/storage/migrations");
    println!("cargo:rerun-if-changed={}", source.display());
    let mut rows = Vec::new();
    for entry in fs::read_dir(&source).expect("migration source directory") {
        let entry = entry.expect("migration directory entry");
        let path = entry.path();
        if path.extension().is_none_or(|ext| ext != "sql") {
            continue;
        }
        assert!(
            entry.file_type().unwrap().is_file(),
            "expected a regular migration file"
        );
        let filename = entry
            .file_name()
            .into_string()
            .expect("UTF-8 migration filename");
        assert!(
            filename
                .bytes()
                .all(|c| c.is_ascii_alphanumeric() || c == b'_' || c == b'.'),
            "invalid migration filename"
        );
        println!("cargo:rerun-if-changed={}", path.display());
        rows.push((filename, path));
    }
    rows.sort_by(|a, b| a.0.cmp(&b.0));
    assert!(!rows.is_empty(), "missing SQL migrations");
    let mut list = String::from("&[\n");
    for (name, path) in rows {
        list.push_str(&format!("({name:?}, include_bytes!({path:?})),\n"));
    }
    list.push(']');
    fs::write(
        PathBuf::from(env::var_os("OUT_DIR").unwrap()).join("migrations.rs"),
        list,
    )
    .unwrap();
}
