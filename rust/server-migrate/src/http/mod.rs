// SPDX-License-Identifier: MPL-2.0
mod bridge;
mod coord_pool;
mod dashboard;
mod devices;
pub(crate) mod integrations;
mod owner;
pub(crate) mod session;
pub use session::Owner;

use axum::{
    Json, Router,
    extract::{DefaultBodyLimit, FromRequest, Request, State},
    http::{HeaderValue, Method, StatusCode, header},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use serde::de::DeserializeOwned;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    collections::HashMap,
    env,
    net::{IpAddr, SocketAddr},
    path::PathBuf,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};
use tokio::{
    net::TcpListener,
    sync::{Semaphore, watch},
    task::JoinSet,
    time::timeout,
};
use url::Url;

pub type Shared = Arc<AppState>;
pub type WebResult<T> = Result<T, WebError>;
#[derive(Debug)]
pub struct WebError(pub StatusCode, pub String);
impl WebError {
    fn internal() -> Self {
        Self(StatusCode::INTERNAL_SERVER_ERROR, "Request failed".into())
    }
}
impl IntoResponse for WebError {
    fn into_response(self) -> Response {
        (self.0, Json(json!({"error":self.1}))).into_response()
    }
}
pub struct ApiJson<T>(pub T);
impl<S: Send + Sync, T: DeserializeOwned> FromRequest<S> for ApiJson<T> {
    type Rejection = WebError;
    async fn from_request(req: Request, state: &S) -> WebResult<Self> {
        let media = req
            .headers()
            .get(header::CONTENT_TYPE)
            .and_then(|h| h.to_str().ok())
            .and_then(|s| s.split(';').next())
            .unwrap_or("");
        if !media.trim().eq_ignore_ascii_case("application/json") {
            return Err(WebError(
                StatusCode::UNSUPPORTED_MEDIA_TYPE,
                "Use application/json".into(),
            ));
        }
        let Json(value) = Json::<T>::from_request(req, state).await.map_err(|e| {
            if e.status() == StatusCode::PAYLOAD_TOO_LARGE {
                WebError(e.status(), "Request too large".into())
            } else {
                WebError(StatusCode::BAD_REQUEST, "Invalid JSON request".into())
            }
        })?;
        Ok(Self(value))
    }
}
pub fn hash(s: &str) -> String {
    hex::encode(Sha256::digest(s.as_bytes()))
}
pub fn random_hex(bytes: usize) -> WebResult<String> {
    let mut data = vec![0; bytes];
    getrandom::fill(&mut data).map_err(|_| WebError::internal())?;
    Ok(hex::encode(data))
}
fn service_actor() -> [String; 2] {
    ["service".into(), "http-server".into()]
}
pub struct AppState {
    pub origin: String,
    owner_hash: String,
    pool: Arc<coord_pool::Pool>,
    socket: PathBuf,
    dashboard: dashboard::Dashboard,
    integrations: integrations::Integrations,
    buckets: Mutex<HashMap<IpAddr, (Instant, u32)>>,
}
impl AppState {
    pub async fn coord(&self, op: &str, actor: [String; 2], input: Value) -> WebResult<Value> {
        self.pool.call(op, actor, input).await
    }
    pub async fn domain(&self, op: &str, actor: [String; 2], input: Value) -> WebResult<Value> {
        bridge::call(&self.socket, op, actor, input).await
    }
    fn allow(&self, ip: IpAddr) -> bool {
        let now = Instant::now();
        let Ok(mut buckets) = self.buckets.lock() else {
            return false;
        };
        if buckets.len() >= 4096 {
            buckets.retain(|_, (start, _)| now.duration_since(*start) < Duration::from_secs(60));
            if buckets.len() >= 4096 && !buckets.contains_key(&ip) {
                return false;
            }
        }
        let bucket = buckets.entry(ip).or_insert((now, 0));
        if now.duration_since(bucket.0) >= Duration::from_secs(60) {
            *bucket = (now, 0)
        }
        if bucket.1 >= 30 {
            return false;
        }
        bucket.1 += 1;
        true
    }
}
async fn protect(State(s): State<Shared>, r: Request, next: Next) -> Response {
    let id = match random_hex(12) {
        Ok(id) => id,
        Err(e) => return e.into_response(),
    };
    let started = Instant::now();
    let route = r.extensions().get::<axum::extract::MatchedPath>().cloned();
    let path = r.uri().path();
    let invalid_origin = r
        .headers()
        .get_all(header::ORIGIN)
        .iter()
        .any(|v| v.as_bytes() != s.origin.as_bytes());
    let cross_site = r
        .headers()
        .get_all("sec-fetch-site")
        .iter()
        .any(|v| v.as_bytes() == b"cross-site");
    let response = if invalid_origin || cross_site {
        WebError(StatusCode::FORBIDDEN, "Permission denied".into()).into_response()
    } else if r.method() == Method::POST
        && matches!(path, "/api/v1/login" | "/api/v1/device/pair")
        && !r
            .extensions()
            .get::<SocketAddr>()
            .is_some_and(|addr| s.allow(addr.ip()))
    {
        WebError(
            StatusCode::TOO_MANY_REQUESTS,
            "Try again in one minute".into(),
        )
        .into_response()
    } else {
        timeout(Duration::from_secs(30), next.run(r))
            .await
            .unwrap_or_else(|_| {
                WebError(
                    StatusCode::SERVICE_UNAVAILABLE,
                    "Request timed out. Refresh its status before retrying.".into(),
                )
                .into_response()
            })
    };
    if response.status().is_server_error() {
        eprintln!(
            "{}",
            json!({"level":"WARN","message":"request failed","id":id,
            "route":route.as_ref().map(|p|p.as_str()).unwrap_or("unmatched"),
            "status":response.status().as_u16(),"duration_ms":started.elapsed().as_millis()})
        );
    }
    let mut res = response;
    let h = res.headers_mut();
    h.insert(
        "x-request-id",
        HeaderValue::from_str(&id).expect("hex request id"),
    );
    h.insert(
        "x-content-type-options",
        HeaderValue::from_static("nosniff"),
    );
    h.insert("referrer-policy", HeaderValue::from_static("no-referrer"));
    h.insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
    h.insert("content-security-policy",HeaderValue::from_static("default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; style-src 'self' 'unsafe-inline'; img-src 'self' data:"));
    res
}
async fn ready(State(s): State<Shared>) -> Response {
    let probe = async {
        s.coord("system.ready", service_actor(), Value::Null)
            .await?;
        s.domain("system.ready", service_actor(), Value::Null).await
    };
    if matches!(timeout(Duration::from_secs(2), probe).await, Ok(Ok(_))) {
        Json(json!({"status":"ready","protocol":1})).into_response()
    } else {
        (
            StatusCode::SERVICE_UNAVAILABLE,
            Json(json!({"status":"unavailable"})),
        )
            .into_response()
    }
}
fn router(s: Shared) -> Router {
    let owner = owner::routes()
        .merge(devices::owner())
        .route("/api/v1/logout", post(session::logout))
        .route_layer(middleware::from_fn_with_state(
            s.clone(),
            session::authenticate,
        ));
    Router::new()
        .merge(owner)
        .merge(devices::public())
        .route("/api/v1/login", post(session::login))
        .layer(DefaultBodyLimit::max(64 << 10))
        .merge(integrations::routes())
        .route(
            "/healthz",
            get(|| async { Json(json!({"status":"alive"})) }),
        )
        .route("/readyz", get(ready))
        .fallback(dashboard::serve)
        .layer(middleware::from_fn_with_state(s.clone(), protect))
        .with_state(s)
}
fn valid_origin(raw: &str) -> bool {
    let Ok(u) = Url::parse(raw) else { return false };
    if !raw.split_once("://").is_some_and(|(_, authority)| {
        !authority.is_empty()
            && !authority.contains(['/', '?', '#', '@', '\\'])
            && !authority.chars().any(char::is_whitespace)
    }) {
        return false;
    }
    if u.cannot_be_a_base()
        || u.host().is_none()
        || !u.username().is_empty()
        || u.password().is_some()
        || u.query().is_some()
        || u.fragment().is_some()
        || u.path() != "/"
        || raw.ends_with('/')
        || !raw.contains("://")
    {
        return false;
    }
    u.scheme() == "https"
        || u.scheme() == "http"
            && u.host().is_some_and(|h| match h {
                url::Host::Domain(d) => d == "localhost",
                url::Host::Ipv4(v) => v.is_loopback(),
                url::Host::Ipv6(v) => v.is_loopback(),
            })
}
async fn serve(
    listener: TcpListener,
    router: Router,
    mut stop: watch::Receiver<bool>,
) -> Result<(), &'static str> {
    use hyper::server::conn::http1;
    use hyper_util::{
        rt::{TokioIo, TokioTimer},
        service::TowerToHyperService,
    };
    let capacity = Arc::new(Semaphore::new(256));
    let mut connections = JoinSet::new();
    loop {
        tokio::select! {
            _=stop.changed()=>break,
            Some(_)=connections.join_next(),if !connections.is_empty()=>{},
            accepted=listener.accept()=>{
                let(socket,peer)=accepted.map_err(|_|"Public listener failed")?;
                let Ok(slot)=capacity.clone().try_acquire_owned() else{continue};
                let app=tower::ServiceBuilder::new().layer(axum::Extension(peer)).service(router.clone());
                let mut stopping=stop.clone();
                connections.spawn(async move{
                    let _slot=slot;
                    let service=TowerToHyperService::new(app);
                    let builder=&mut http1::Builder::new();
                    builder.timer(TokioTimer::new()).header_read_timeout(Duration::from_secs(5)).max_buf_size(16<<10);
                    let mut io=tokio_io_timeout::TimeoutStream::new(socket);
                    io.set_write_timeout(Some(Duration::from_secs(30)));
                    io.set_read_timeout(Some(Duration::from_secs(90)));
                    let conn=builder.serve_connection(TokioIo::new(Box::pin(io)),service);tokio::pin!(conn);
                    tokio::select!{_=conn.as_mut()=>{},_=stopping.changed()=>{conn.as_mut().graceful_shutdown();let _=conn.await;}}
                });
            }
        }
    }
    let _ = timeout(Duration::from_secs(10), async {
        while connections.join_next().await.is_some() {}
    })
    .await;
    Ok(())
}
pub async fn run() -> Result<(), &'static str> {
    let origin = env::var("BUGSNITCH_ORIGIN").unwrap_or_else(|_| "http://localhost:8787".into());
    let owner = env::var("BUGSNITCH_OWNER_TOKEN").unwrap_or_default();
    let dsn = env::var("DATABASE_URL").map_err(|_| "Set DATABASE_URL")?;
    if !valid_origin(&origin) || owner.len() < 32 {
        return Err("Set a valid BUGSNITCH_ORIGIN and owner token of at least 32 characters");
    }
    let integrations = integrations::from_env()?;
    if dsn.is_empty() {
        return Err("Set DATABASE_URL");
    }
    let pool = coord_pool::Pool::new()?;
    let mut worker = bridge::Worker::start(&origin).await?;
    let state = Arc::new(AppState {
        origin,
        owner_hash: hash(&owner),
        pool,
        socket: worker.socket.clone(),
        dashboard: dashboard::Dashboard::load(&PathBuf::from(
            env::var_os("BUGSNITCH_WEB_DIR").unwrap_or_else(|| "web/dist".into()),
        )),
        integrations,
        buckets: Default::default(),
    });
    timeout(
        Duration::from_secs(5),
        state.coord("system.http_ready", service_actor(), Value::Null),
    )
    .await
    .map_err(|_| "Database readiness timed out")?
    .map_err(|_| "Database unavailable")?;
    let listener =
        TcpListener::bind(env::var("BUGSNITCH_LISTEN").unwrap_or_else(|_| "127.0.0.1:8787".into()))
            .await
            .map_err(|_| "Cannot bind public HTTP listener")?;
    eprintln!(
        "{}",
        json!({"level":"INFO","message":"listening","protocol":1,"address":listener.local_addr().ok()})
    );
    let (stop, rx) = watch::channel(false);
    let mut serving = tokio::spawn(serve(listener, router(state), rx));
    let mut terminate = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
        .map_err(|_| "Cannot register shutdown signal")?;
    tokio::select! {
        _=terminate.recv()=>{},
        _=tokio::signal::ctrl_c()=>{},
        _=worker.child.wait()=>return Err("Private domain worker exited"),
        result=&mut serving=>return result.map_err(|_|"HTTP service interrupted")?,
    }
    let _ = stop.send(true);
    let result = serving.await.map_err(|_| "HTTP service interrupted")?;
    drop(worker.lifeline.take());
    let _ = timeout(Duration::from_secs(5), worker.child.wait()).await;
    result
}

#[cfg(test)]
mod tests {
    use super::valid_origin;
    #[test]
    fn origins_are_exact_https_authorities_or_loopback_http() {
        for allowed in [
            "https://bugsnitch.com",
            "https://host.test:8443",
            "http://localhost:8787",
            "http://127.0.0.1",
            "http://[::1]:8787",
        ] {
            assert!(valid_origin(allowed), "{allowed}");
        }
        for denied in [
            "http://outside.example",
            "https://alice@example.test",
            "https://@example.test",
            "file:///tmp/socket",
            "http://localhost.evil.test",
            "http://localhost?query",
            "https://host.test#fragment",
            "https://host.test/",
            "https://host.test/a/..",
            "https://host.test\\x",
        ] {
            assert!(!valid_origin(denied), "{denied}");
        }
    }
}
