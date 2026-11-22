// SPDX-License-Identifier: MPL-2.0

// libpq owns connection configuration, authentication, TLS and certificate
// handling. Only pgx's extra search_path keyword is removed and applied as a
// bound PostgreSQL value inside the migration transaction.
pub struct Config {
    pub native: String,
    pub search_path: Option<String>,
}

pub fn split(dsn: &str) -> Result<Config, ()> {
    if dsn.is_empty() || dsn.as_bytes().contains(&0) {
        return Err(());
    }
    if dsn.starts_with("postgres://") || dsn.starts_with("postgresql://") {
        let Some((base, query)) = dsn.split_once('?') else {
            return Ok(Config {
                native: dsn.to_owned(),
                search_path: None,
            });
        };
        let mut search_path = None;
        let mut rest = Vec::new();
        for pair in query.split('&') {
            let (k, v) = pair.split_once('=').unwrap_or((pair, ""));
            if form_decode(k)? == "search_path" {
                search_path = Some(form_decode(v)?);
            } else {
                rest.push(pair);
            }
        }
        let native = if rest.is_empty() {
            base.to_owned()
        } else {
            format!("{base}?{}", rest.join("&"))
        };
        return Ok(Config {
            native,
            search_path,
        });
    }
    keyword_dsn(dsn)
}

fn form_decode(s: &str) -> Result<String, ()> {
    let bytes = s.as_bytes();
    let mut decoded = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        match bytes[i] {
            b'%' => {
                let hex = bytes.get(i + 1..i + 3).ok_or(())?;
                let n = u8::from_str_radix(std::str::from_utf8(hex).map_err(|_| ())?, 16)
                    .map_err(|_| ())?;
                if n == 0 {
                    return Err(());
                }
                decoded.push(n);
                i += 3;
            }
            b'+' => {
                decoded.push(b' ');
                i += 1;
            }
            n => {
                decoded.push(n);
                i += 1;
            }
        }
    }
    String::from_utf8(decoded).map_err(|_| ())
}

fn keyword_dsn(dsn: &str) -> Result<Config, ()> {
    let bytes = dsn.as_bytes();
    let mut i = 0;
    let mut fields = Vec::new();
    let mut search_path = None;
    while i < bytes.len() {
        while i < bytes.len() && bytes[i].is_ascii_whitespace() {
            i += 1;
        }
        if i == bytes.len() {
            break;
        }
        let start = i;
        while i < bytes.len() && bytes[i] != b'=' && !bytes[i].is_ascii_whitespace() {
            i += 1;
        }
        let key = &dsn[start..i];
        while i < bytes.len() && bytes[i].is_ascii_whitespace() {
            i += 1;
        }
        if bytes.get(i) != Some(&b'=') || key.is_empty() {
            return Err(());
        }
        i += 1;
        while i < bytes.len() && bytes[i].is_ascii_whitespace() {
            i += 1;
        }
        let quoted = bytes.get(i) == Some(&b'\'');
        if quoted {
            i += 1;
        }
        let mut value = Vec::new();
        loop {
            let Some(&c) = bytes.get(i) else {
                if quoted {
                    return Err(());
                }
                break;
            };
            if quoted && c == b'\'' {
                i += 1;
                break;
            }
            if !quoted && c.is_ascii_whitespace() {
                break;
            }
            i += 1;
            let literal = if c == b'\\' {
                let next = *bytes.get(i).ok_or(())?;
                i += 1;
                next
            } else {
                c
            };
            if key == "search_path" {
                value.push(literal);
            }
        }
        if i < bytes.len() && !bytes[i].is_ascii_whitespace() {
            return Err(());
        }
        if key == "search_path" {
            search_path = Some(String::from_utf8(value).map_err(|_| ())?);
        } else {
            fields.push(&dsn[start..i]);
        }
    }
    Ok(Config {
        native: fields.join(" "),
        search_path,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn preserves_socket_and_tls_parameters() {
        let cfg=split("postgres:///db?host=%2Ftmp&sslmode=verify-full&search_path=one%2C+two&sslrootcert=%2Fa%20b").unwrap();
        assert_eq!(
            cfg.native,
            "postgres:///db?host=%2Ftmp&sslmode=verify-full&sslrootcert=%2Fa%20b"
        );
        assert_eq!(cfg.search_path.as_deref(), Some("one, two"));
    }
    #[test]
    fn distinguishes_quoted_values_from_keys() {
        let cfg=split(r#"host=/tmp password='value search_path=x' search_path='"My Schema", public' options='-c statement_timeout=22'"#).unwrap();
        assert_eq!(
            cfg.native,
            r#"host=/tmp password='value search_path=x' options='-c statement_timeout=22'"#
        );
        assert_eq!(cfg.search_path.as_deref(), Some(r#""My Schema", public"#));
    }
}
