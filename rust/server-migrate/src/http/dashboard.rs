// SPDX-License-Identifier: MPL-2.0
use super::{Shared, WebError, random_hex};
use axum::{
    body::Body,
    extract::State,
    http::{HeaderMap, Method, StatusCode, Uri, header},
    response::{IntoResponse, Response},
};
use bytes::Bytes;
use http_range::{HttpRange, HttpRangeParseError};
use percent_encoding::percent_decode_str;
use rustix::fs::{Dir, FileType, Mode, OFlags, openat};
use rustix::io::Errno;
use std::{
    collections::HashMap,
    fs::File,
    io::{self, Read},
    path::Path,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

struct Asset {
    data: Bytes,
    mime: String,
    modified: Option<SystemTime>,
}
pub(super) struct Dashboard(HashMap<String, Asset>);

// Open each child relative to a pinned directory descriptor. A directory or
// file replaced with a symlink during deployment cannot escape this snapshot.
fn visit(dir: File, prefix: &str, files: &mut HashMap<String, Asset>) -> io::Result<()> {
    for entry in Dir::read_from(&dir)? {
        let entry = entry?;
        let name = entry.file_name();
        if name.to_bytes().starts_with(b".") {
            continue;
        }
        let kind = entry.file_type();
        if !matches!(
            kind,
            FileType::Directory | FileType::RegularFile | FileType::Unknown
        ) {
            continue;
        }
        let mut flags = OFlags::RDONLY | OFlags::CLOEXEC | OFlags::NOFOLLOW | OFlags::NONBLOCK;
        if kind == FileType::Directory {
            flags |= OFlags::DIRECTORY;
        }
        let mut file = match openat(&dir, name, flags, Mode::empty()) {
            Ok(fd) => File::from(fd),
            Err(Errno::NOENT | Errno::LOOP | Errno::NOTDIR) => continue,
            Err(e) => return Err(e.into()),
        };
        let path = format!("{prefix}/{}", name.to_str().map_err(io::Error::other)?);
        let metadata = file.metadata()?;
        if metadata.is_dir() {
            visit(file, &path, files)?;
        } else if metadata.is_file() {
            let seconds = metadata
                .modified()
                .ok()
                .and_then(|t| t.duration_since(UNIX_EPOCH).ok())
                .map(|t| t.as_secs());
            // HTTP dates have second resolution and a four-digit year.
            let modified = seconds
                .filter(|t| (1..=253402300799).contains(t))
                .map(|t| UNIX_EPOCH + Duration::from_secs(t));
            let mut data = Vec::new();
            file.read_to_end(&mut data)?;
            let mime = mime_guess::from_path(&path)
                .first_or_octet_stream()
                .to_string();
            files.insert(
                path,
                Asset {
                    data: data.into(),
                    modified,
                    mime,
                },
            );
        }
    }
    Ok(())
}

impl Dashboard {
    pub fn load(root: &Path) -> Self {
        let mut files = HashMap::new();
        if File::open(root)
            .and_then(|dir| visit(dir, "", &mut files))
            .is_err()
            || !files.contains_key("/index.html")
        {
            files.clear();
        }
        Self(files)
    }

    fn respond(&self, method: Method, uri: &Uri, headers: &HeaderMap) -> Response {
        let Ok(path) = percent_decode_str(uri.path()).decode_utf8() else {
            return StatusCode::NOT_FOUND.into_response();
        };
        if path == "/api" || path.starts_with("/api/") {
            return WebError(StatusCode::NOT_FOUND, "Unknown API operation".into()).into_response();
        }
        if method != Method::GET && method != Method::HEAD {
            return (
                StatusCode::METHOD_NOT_ALLOWED,
                [(header::ALLOW, "GET, HEAD")],
            )
                .into_response();
        }
        if path.ends_with("/index.html") && self.0.contains_key(path.as_ref()) {
            let to = uri
                .query()
                .filter(|q| !q.is_empty())
                .map(|q| format!("./?{q}"))
                .unwrap_or_else(|| "./".into());
            return (StatusCode::MOVED_PERMANENTLY, [(header::LOCATION, to)]).into_response();
        }
        let asset = self.0.get(path.as_ref()).or_else(|| {
            if path.rsplit('/').next().unwrap_or("").contains('.') {
                None
            } else {
                self.0.get("/index.html")
            }
        });
        match asset {
            Some(asset) => asset.serve(headers, method == Method::HEAD),
            None => StatusCode::NOT_FOUND.into_response(),
        }
    }
}

impl Asset {
    fn serve(&self, headers: &HeaderMap, head: bool) -> Response {
        let header_date = |key| {
            headers
                .get(key)
                .and_then(|v| v.to_str().ok())
                .and_then(|s| httpdate::parse_http_date(s).ok())
        };
        let mut response = Response::builder();
        if let Some(modified) = self.modified {
            response = response.header(header::LAST_MODIFIED, httpdate::fmt_http_date(modified));
        }
        // The bundle has a last-modified validator, but does not issue ETags.
        let failed = match headers.get(header::IF_MATCH) {
            Some(value) => value.as_bytes() != b"*",
            None => self
                .modified
                .zip(header_date(header::IF_UNMODIFIED_SINCE))
                .is_some_and(|(modified, since)| modified > since),
        };
        let unchanged = match headers.get(header::IF_NONE_MATCH) {
            Some(value) => value.as_bytes() == b"*",
            None => self
                .modified
                .zip(header_date(header::IF_MODIFIED_SINCE))
                .is_some_and(|(modified, since)| modified <= since),
        };
        if failed || unchanged {
            let status = if failed {
                StatusCode::PRECONDITION_FAILED
            } else {
                StatusCode::NOT_MODIFIED
            };
            return response
                .status(status)
                .body(Body::empty())
                .expect("static response headers");
        }
        let size = self.data.len() as u64;
        let allow_range = !headers.contains_key(header::IF_RANGE)
            || self
                .modified
                .zip(header_date(header::IF_RANGE))
                .is_some_and(|(modified, since)| modified == since);
        let mut ranges = Vec::new();
        if let Some(value) = headers.get(header::RANGE).filter(|_| allow_range) {
            match HttpRange::parse_bytes(value.as_bytes(), size) {
                // A server may ignore overlapping or excessive range requests.
                Ok(parts)
                    if parts.len() <= 16
                        && parts
                            .iter()
                            .try_fold(0u64, |n, p| n.checked_add(p.length))
                            .is_some_and(|n| n <= size) =>
                {
                    ranges = parts.into_iter().filter(|p| p.length > 0).collect()
                }
                Ok(_) => (),
                Err(HttpRangeParseError::NoOverlap) if size == 0 => (),
                Err(e) => {
                    let mut failure = Response::builder().status(StatusCode::RANGE_NOT_SATISFIABLE);
                    if matches!(e, HttpRangeParseError::NoOverlap) {
                        failure = failure.header(header::CONTENT_RANGE, format!("bytes */{size}"));
                    }
                    return failure.body(Body::empty()).expect("static range headers");
                }
            }
        }
        response = response.header(header::ACCEPT_RANGES, "bytes");
        let (mime, parts) = match ranges.as_slice() {
            [] => (self.mime.clone(), vec![self.data.clone()]),
            [part] => {
                response = response
                    .status(StatusCode::PARTIAL_CONTENT)
                    .header(header::CONTENT_RANGE, content_range(part, size));
                (self.mime.clone(), vec![self.part(part)])
            }
            _ => {
                let boundary = match random_hex(18) {
                    Ok(b) => b,
                    Err(e) => return e.into_response(),
                };
                response = response.status(StatusCode::PARTIAL_CONTENT);
                let mut parts = Vec::new();
                for part in ranges {
                    parts.push(
                        format!(
                            "--{boundary}\r\nContent-Range: {}\r\nContent-Type: {}\r\n\r\n",
                            content_range(&part, size),
                            self.mime
                        )
                        .into(),
                    );
                    parts.push(self.part(&part));
                    parts.push(Bytes::from_static(b"\r\n"));
                }
                parts.push(format!("--{boundary}--\r\n").into());
                (format!("multipart/byteranges; boundary={boundary}"), parts)
            }
        };
        let length: usize = parts.iter().map(Bytes::len).sum();
        let body = if head {
            Body::empty()
        } else {
            Body::from_stream(futures_util::stream::iter(
                parts.into_iter().map(Ok::<_, io::Error>),
            ))
        };
        response
            .header(header::CONTENT_LENGTH, length)
            .header(header::CONTENT_TYPE, mime)
            .body(body)
            .expect("static response headers")
    }

    fn part(&self, part: &HttpRange) -> Bytes {
        self.data
            .slice(part.start as usize..(part.start + part.length) as usize)
    }
}

fn content_range(part: &HttpRange, size: u64) -> String {
    format!(
        "bytes {}-{}/{size}",
        part.start,
        part.start + part.length - 1
    )
}

pub(super) async fn serve(
    State(s): State<Shared>,
    method: Method,
    uri: Uri,
    headers: HeaderMap,
) -> Response {
    s.dashboard.respond(method, &uri, &headers)
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::to_bytes;
    use std::{fs, os::unix::fs::symlink};

    fn fixture() -> (tempfile::TempDir, Dashboard) {
        let root = tempfile::tempdir().unwrap();
        fs::write(root.path().join("index.html"), "dashboard").unwrap();
        fs::write(root.path().join("asset with space.js"), "0123456789").unwrap();
        let dashboard = Dashboard::load(root.path());
        (root, dashboard)
    }

    fn request(
        dashboard: &Dashboard,
        method: Method,
        path: &str,
        headers: &[(&str, &str)],
    ) -> Response {
        let mut map = HeaderMap::new();
        for &(name, value) in headers {
            map.insert(
                header::HeaderName::from_bytes(name.as_bytes()).unwrap(),
                value.parse().unwrap(),
            );
        }
        dashboard.respond(method, &path.parse().unwrap(), &map)
    }

    async fn body(response: Response) -> Bytes {
        to_bytes(response.into_body(), 4096).await.unwrap()
    }

    const ASSET: &str = "/asset%20with%20space.js";

    #[tokio::test]
    async fn serves_byte_ranges_and_conditional_requests() {
        let (_root, dashboard) = fixture();
        let full = request(&dashboard, Method::GET, ASSET, &[]);
        let date = full.headers()[header::LAST_MODIFIED]
            .to_str()
            .unwrap()
            .to_string();
        assert_eq!(full.headers()[header::ACCEPT_RANGES], "bytes");
        assert_eq!(body(full).await, "0123456789");

        let ranged = request(
            &dashboard,
            Method::GET,
            ASSET,
            &[("range", "bytes=2-5"), ("if-range", &date)],
        );
        assert_eq!(ranged.status(), StatusCode::PARTIAL_CONTENT);
        assert_eq!(ranged.headers()[header::CONTENT_RANGE], "bytes 2-5/10");
        assert_eq!(body(ranged).await, "2345");
        let head = request(&dashboard, Method::HEAD, ASSET, &[("range", "bytes=-2")]);
        assert_eq!(head.headers()[header::CONTENT_LENGTH], "2");
        assert!(body(head).await.is_empty());

        let multiple = request(&dashboard, Method::GET, ASSET, &[("range", "bytes=0-0,8-")]);
        assert_eq!(multiple.status(), StatusCode::PARTIAL_CONTENT);
        assert!(
            multiple.headers()[header::CONTENT_TYPE]
                .to_str()
                .unwrap()
                .starts_with("multipart/byteranges; boundary=")
        );
        let length: usize = multiple.headers()[header::CONTENT_LENGTH]
            .to_str()
            .unwrap()
            .parse()
            .unwrap();
        let bytes = body(multiple).await;
        assert_eq!(bytes.len(), length);
        let multipart = std::str::from_utf8(&bytes).unwrap();
        assert!(
            multipart.contains("Content-Range: bytes 8-9/10\r\n")
                && multipart.contains("\r\n\r\n89\r\n")
        );

        let invalid = request(&dashboard, Method::GET, ASSET, &[("range", "bytes=20-")]);
        assert_eq!(invalid.status(), StatusCode::RANGE_NOT_SATISFIABLE);
        assert_eq!(invalid.headers()[header::CONTENT_RANGE], "bytes */10");
        for (headers, expected) in [
            (
                vec![("if-modified-since", date.as_str())],
                StatusCode::NOT_MODIFIED,
            ),
            (
                vec![("if-unmodified-since", "Thu, 01 Jan 1970 00:00:01 GMT")],
                StatusCode::PRECONDITION_FAILED,
            ),
            (
                vec![
                    ("if-range", "Thu, 01 Jan 1970 00:00:01 GMT"),
                    ("range", "bytes=0-0"),
                ],
                StatusCode::OK,
            ),
            (vec![("if-none-match", "*")], StatusCode::NOT_MODIFIED),
            (
                vec![
                    ("if-none-match", "\"other\""),
                    ("if-modified-since", date.as_str()),
                ],
                StatusCode::OK,
            ),
            (
                vec![("if-match", "\"other\"")],
                StatusCode::PRECONDITION_FAILED,
            ),
        ] {
            assert_eq!(
                request(&dashboard, Method::GET, ASSET, &headers).status(),
                expected
            );
        }
    }

    #[tokio::test]
    async fn snapshots_assets_without_following_symlinks_and_decodes_paths() {
        let (root, _) = fixture();
        let outside = tempfile::tempdir().unwrap();
        fs::write(outside.path().join("private.txt"), "private").unwrap();
        symlink(outside.path(), root.path().join("external")).unwrap();
        symlink(
            outside.path().join("private.txt"),
            root.path().join("external.txt"),
        )
        .unwrap();
        fs::write(root.path().join(".env"), "private").unwrap();
        let bundle = Dashboard::load(root.path());
        for name in [
            "/external/private.txt",
            "/external.txt",
            "/%2eenv",
            "/api%2funknown",
        ] {
            assert_eq!(
                request(&bundle, Method::GET, name, &[]).status(),
                StatusCode::NOT_FOUND
            );
        }
        fs::remove_file(root.path().join("asset with space.js")).unwrap();
        symlink(
            outside.path().join("private.txt"),
            root.path().join("asset with space.js"),
        )
        .unwrap();
        assert_eq!(
            body(request(&bundle, Method::GET, ASSET, &[])).await,
            "0123456789"
        );
        assert_eq!(
            body(request(&bundle, Method::GET, "/plans/current", &[])).await,
            "dashboard"
        );
        let redirect = request(&bundle, Method::GET, "/%69ndex.html?x=y", &[]);
        assert_eq!(redirect.status(), StatusCode::MOVED_PERMANENTLY);
        assert_eq!(redirect.headers()[header::LOCATION], "./?x=y");
    }
}
