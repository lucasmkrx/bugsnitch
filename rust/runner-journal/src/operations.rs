// SPDX-License-Identifier: MPL-2.0
use serde_json::{Value, json};
use std::io::{self, Read, Write};

pub(super) fn serve(operation: fn(Value) -> Result<Value, String>) -> io::Result<()> {
    const LIMIT: u64 = 256 << 10;
    let mut input = Vec::new();
    io::stdin().lock().take(LIMIT + 1).read_to_end(&mut input)?;
    let request: Option<Value> = if input.len() as u64 <= LIMIT {
        serde_json::from_slice(&input).ok()
    } else {
        None
    };
    let result = match request {
        Some(request) if request.get("version").and_then(Value::as_u64) == Some(1) => {
            operation(request)
        }
        _ => Err("invalid native runner request or protocol version".into()),
    };
    let reply = match result {
        Ok(value) => json!({"version":1,"value":value}),
        Err(error) => json!({"version":1,"error":error}),
    };
    let mut stdout = io::stdout().lock();
    serde_json::to_writer(&mut stdout, &reply)?;
    stdout.flush()
}
