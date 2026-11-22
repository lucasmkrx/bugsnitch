// SPDX-License-Identifier: MPL-2.0
use base64::{
    Engine,
    engine::general_purpose::{STANDARD, URL_SAFE_NO_PAD},
};
use ring::{rand::SystemRandom, rsa::KeyPair, signature::RSA_PKCS1_SHA256};
use serde::{Serialize, de::DeserializeOwned};
use serde_json::{Value, json};
use std::{
    env,
    fs::File,
    io::Read,
    net::SocketAddr,
    time::{Duration, SystemTime, UNIX_EPOCH},
};
use ureq::{Agent, http::HeaderMap};

pub const VERSION: &str = "2026-03-10";
const MAX_BODY: u64 = 8 << 20;
pub type Result<T> = std::result::Result<T, Error>;

// Only fixed error categories and retry metadata cross the private protocol.
#[derive(Debug, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum Error {
    Configuration,
    Invalid,
    Connection,
    Response,
    SubjectChanged,
    Http { status: u16, retry_seconds: u64 },
}

pub struct Client {
    key: KeyPair,
    client_id: String,
    agent: Agent,
    base: String,
}

fn load_key() -> Result<KeyPair> {
    let file = env::var_os("GITHUB_APP_KEY_FILE").ok_or(Error::Configuration)?;
    let mut pem = String::new();
    File::open(file)
        .and_then(|f| f.take(32_769).read_to_string(&mut pem))
        .map_err(|_| Error::Configuration)?;
    if pem.len() > 32_768 {
        return Err(Error::Configuration);
    }
    let pem = pem.trim();
    for (label, pkcs8) in [("RSA PRIVATE KEY", false), ("PRIVATE KEY", true)] {
        if let Some(data) = pem
            .strip_prefix(&format!("-----BEGIN {label}-----"))
            .and_then(|s| s.strip_suffix(&format!("-----END {label}-----")))
        {
            let bytes = STANDARD
                .decode(data.split_ascii_whitespace().collect::<String>())
                .map_err(|_| Error::Configuration)?;
            return if pkcs8 {
                KeyPair::from_pkcs8(&bytes)
            } else {
                KeyPair::from_der(&bytes)
            }
            .map_err(|_| Error::Configuration);
        }
    }
    Err(Error::Configuration)
}

fn unix_time() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
}

fn retry_after(h: &HeaderMap) -> u64 {
    let number = |key: &'static str| h.get(key)?.to_str().ok()?.parse::<u64>().ok();
    if let Some(wait) = number("retry-after").filter(|n| (1..=86_400).contains(n)) {
        return wait;
    }
    if number("x-ratelimit-remaining") == Some(0)
        && let Some(wait) = number("x-ratelimit-reset")
            .and_then(|n| n.checked_sub(unix_time()))
            .filter(|n| *n <= 86_400)
    {
        return wait + 1;
    }
    0
}

impl Client {
    pub fn new(fixture: Option<&str>) -> Result<Self> {
        let key = load_key()?;
        let client_id = env::var("GITHUB_APP_CLIENT_ID").map_err(|_| Error::Configuration)?;
        if client_id.is_empty() || client_id.len() > 256 {
            return Err(Error::Configuration);
        }
        let base = match fixture {
            None => "https://api.github.com".to_owned(),
            Some(value) => {
                let addr: SocketAddr = value
                    .strip_prefix("http://")
                    .ok_or(Error::Configuration)?
                    .parse()
                    .map_err(|_| Error::Configuration)?;
                if addr.port() == 0 || !addr.ip().is_loopback() {
                    return Err(Error::Configuration);
                }
                format!("http://{addr}")
            }
        };
        let agent = Agent::config_builder()
            .user_agent("Bugsnitch/1")
            .timeout_global(Some(Duration::from_secs(15)))
            .max_redirects(0)
            .http_status_as_error(false);
        let agent = if fixture.is_some() {
            agent.proxy(None)
        } else {
            agent
        }
        .build()
        .into();
        Ok(Self {
            key,
            client_id,
            agent,
            base,
        })
    }
    pub fn jwt(&self) -> Result<String> {
        let now = unix_time();
        let claims = json!({"iss":self.client_id,"iat":now.saturating_sub(60),"exp":now+480});
        let header = URL_SAFE_NO_PAD.encode(br#"{"alg":"RS256","typ":"JWT"}"#);
        let content = format!("{header}.{}", URL_SAFE_NO_PAD.encode(claims.to_string()));
        let mut sig = vec![0; self.key.public().modulus_len()];
        self.key
            .sign(
                &RSA_PKCS1_SHA256,
                &SystemRandom::new(),
                content.as_bytes(),
                &mut sig,
            )
            .map_err(|_| Error::Configuration)?;
        Ok(format!("{content}.{}", URL_SAFE_NO_PAD.encode(sig)))
    }
    pub fn request<T: DeserializeOwned>(
        &self,
        token: &str,
        route: &str,
        body: Option<&Value>,
        expected: u16,
    ) -> Result<T> {
        let url = format!("{}{route}", self.base);
        let bearer = format!("Bearer {token}");
        let mut resp = if let Some(payload) = body {
            self.agent
                .post(&url)
                .header("Authorization", &bearer)
                .header("X-GitHub-Api-Version", VERSION)
                .header("Accept", "application/vnd.github+json")
                .header("Content-Type", "application/json")
                .send(payload.to_string().as_bytes())
        } else {
            self.agent
                .get(&url)
                .header("Authorization", &bearer)
                .header("X-GitHub-Api-Version", VERSION)
                .header("Accept", "application/vnd.github+json")
                .call()
        }
        .map_err(|_| Error::Connection)?;
        let status = resp.status().as_u16();
        if status != expected {
            return Err(Error::Http {
                status,
                retry_seconds: retry_after(resp.headers()),
            });
        }
        let bytes = resp
            .body_mut()
            .with_config()
            .limit(MAX_BODY)
            .read_to_vec()
            .map_err(|_| Error::Response)?;
        serde_json::from_slice(&bytes).map_err(|_| Error::Response)
    }
}

pub fn escape_component(value: &str) -> String {
    use std::fmt::Write;
    let mut escaped = String::with_capacity(value.len());
    for c in value.bytes() {
        if c.is_ascii_alphanumeric() || b"-._~".contains(&c) {
            escaped.push(c as char);
        } else {
            write!(escaped, "%{c:02X}").expect("string write");
        }
    }
    escaped
}
