// SPDX-License-Identifier: MPL-2.0
use super::{session::field, *};
use crate::coord::{Db, Result as DbResult};
use gcp_auth::{MetadataServiceAccount, TokenProvider};
use hmac::{Hmac, KeyInit, Mac};
use serde::Deserialize;
use std::sync::LazyLock;
use subtle::ConstantTimeEq;

const ACTION: &str = "email_sign_in";
const REQUEST_TIMEOUT: Duration = Duration::from_secs(3);

pub(super) struct Settings {
    project: String,
    site_key: String,
    resend_key: String,
    sender: String,
    code_key: [u8; 32],
    credentials: tokio::sync::OnceCell<MetadataServiceAccount>,
    client: reqwest::Client,
}

impl Settings {
    pub(super) fn from_env() -> Result<Option<Self>, &'static str> {
        let [project, site_key, resend_key, sender, key] = [
            "BUGSNITCH_RECAPTCHA_PROJECT",
            "BUGSNITCH_RECAPTCHA_SITE_KEY",
            "BUGSNITCH_RESEND_API_KEY",
            "BUGSNITCH_EMAIL_FROM",
            "BUGSNITCH_EMAIL_CODE_KEY",
        ]
        .map(|name| env::var(name).unwrap_or_default());
        let values = [&project, &site_key, &resend_key, &sender, &key];
        if values.iter().all(|value| value.is_empty()) {
            return Ok(None);
        }
        if values.iter().any(|value| value.is_empty())
            || !(6..=30).contains(&project.len())
            || !project
                .bytes()
                .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-')
            || !valid_site_key(&site_key)
            || address(&sender).is_err()
        {
            return Err("Set all five email sign-in settings with valid values");
        }
        let mut code_key = [0; 32];
        hex::decode_to_slice(key, &mut code_key)
            .map_err(|_| "BUGSNITCH_EMAIL_CODE_KEY must be 32 bytes encoded as hex")?;
        let client = reqwest::Client::builder()
            .https_only(true)
            .no_proxy()
            .redirect(reqwest::redirect::Policy::none())
            .timeout(REQUEST_TIMEOUT)
            .build()
            .map_err(|_| "Cannot initialize the email sign-in HTTP client")?;
        Ok(Some(Self {
            project,
            site_key,
            resend_key,
            sender,
            code_key,
            credentials: tokio::sync::OnceCell::new(),
            client,
        }))
    }

    async fn captcha(&self, response: &str, hostname: &str) -> WebResult<()> {
        let credential = tokio::time::timeout(REQUEST_TIMEOUT, async {
            let credentials = self
                .credentials
                .get_or_try_init(MetadataServiceAccount::new)
                .await?;
            credentials.token(&[]).await
        })
        .await
        .map_err(|_| unavailable())?
        .map_err(|_| unavailable())?;
        let assessment: Assessment = self.client
            .post(format!("https://recaptchaenterprise.googleapis.com/v1/projects/{}/assessments", self.project))
            .bearer_auth(credential.as_str())
            .json(&json!({"event":{"token":response,"siteKey":self.site_key,"expectedAction":ACTION}}))
            .send().await.and_then(reqwest::Response::error_for_status)
            .map_err(|_| unavailable())?
            .json().await.map_err(|_| unavailable())?;
        if assessment.accepts(hostname) {
            Ok(())
        } else {
            Err(WebError(
                StatusCode::BAD_REQUEST,
                "Could not verify reCAPTCHA. Try again.".into(),
            ))
        }
    }

    async fn send_code(&self, to: &str, code: &str, challenge: &str) {
        let result = self.client.post("https://api.resend.com/emails")
            .bearer_auth(&self.resend_key)
            .header("Idempotency-Key", format!("bugsnitch-email-sign-in/{challenge}"))
            .json(&json!({
                "from": self.sender,
                "to": [to],
                "subject": "Your Bugsnitch sign-in code",
                "text": format!("Your Bugsnitch sign-in code is {code}. It expires in 10 minutes. If you did not request it, you can ignore this email.")
            }))
            .send().await;
        if !result.is_ok_and(|response| response.status().is_success()) {
            eprintln!(r#"{{"level":"WARN","message":"Email sign-in delivery request failed"}}"#);
        }
    }
}

fn unavailable() -> WebError {
    WebError(
        StatusCode::SERVICE_UNAVAILABLE,
        "Email sign-in is temporarily unavailable. Try again later.".into(),
    )
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Assessment {
    token_properties: TokenProperties,
    risk_analysis: Option<RiskAnalysis>,
}
#[derive(Deserialize)]
struct TokenProperties {
    valid: bool,
    action: Option<String>,
    hostname: Option<String>,
}
#[derive(Deserialize)]
struct RiskAnalysis {
    score: Option<f32>,
}
impl Assessment {
    fn accepts(&self, hostname: &str) -> bool {
        self.token_properties.valid
            && self.token_properties.action.as_deref() == Some(ACTION)
            && self.token_properties.hostname.as_deref() == Some(hostname)
            && self
                .risk_analysis
                .as_ref()
                .and_then(|risk| risk.score)
                .is_some_and(|score| (0.5..=1.0).contains(&score))
    }
}

fn valid_site_key(key: &str) -> bool {
    key.len() == 40
        && key
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"-_".contains(&b))
}

pub(super) fn address(value: &str) -> WebResult<String> {
    static EMAIL: LazyLock<regex::Regex> = LazyLock::new(|| {
        regex::Regex::new(
        r"^[a-z0-9_+'%-]+(?:\.[a-z0-9_+'%-]+)*@(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$"
    ).expect("email address pattern")
    });
    let value = value.trim();
    if value.len() > 254
        || !EMAIL.is_match(&value.to_ascii_lowercase())
        || value.find('@').is_none_or(|i| i > 64)
    {
        return Err(WebError(
            StatusCode::BAD_REQUEST,
            "Enter a valid email address".into(),
        ));
    }
    Ok(value.into())
}

fn code() -> WebResult<String> {
    loop {
        let mut bytes = [0; 4];
        getrandom::fill(&mut bytes).map_err(|_| WebError::internal())?;
        let number = u32::from_ne_bytes(bytes);
        if number < 4_294_000_000 {
            return Ok(format!("{:06}", number % 1_000_000));
        }
    }
}

fn code_mac(key: &[u8; 32], challenge: &str, code: &str) -> String {
    let mut mac = Hmac::<Sha256>::new_from_slice(key).expect("HMAC accepts a 32-byte key");
    mac.update(challenge.as_bytes());
    mac.update(code.as_bytes());
    hex::encode(mac.finalize().into_bytes())
}

pub(super) fn routes() -> Router<Shared> {
    Router::new()
        .route("/api/v1/login/email", get(configuration))
        .route("/api/v1/login/email/start", post(start))
        .route("/api/v1/login/email/verify", post(verify))
}

async fn configuration(State(s): State<Shared>) -> Json<Value> {
    Json(json!({"siteKey":s.email_sign_in.as_ref().map(|settings| &settings.site_key)}))
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct Start {
    email: String,
    captcha_token: String,
}

async fn start(State(s): State<Shared>, ApiJson(b): ApiJson<Start>) -> WebResult<Json<Value>> {
    let settings = s.email_sign_in.as_ref().ok_or_else(unavailable)?;
    let email = address(&b.email)?;
    if b.captcha_token.is_empty() || b.captcha_token.len() > 4096 {
        return Err(WebError(
            StatusCode::BAD_REQUEST,
            "Complete reCAPTCHA and try again".into(),
        ));
    }
    let origin = Url::parse(&s.origin).map_err(|_| WebError::internal())?;
    settings
        .captcha(
            &b.captcha_token,
            origin.host_str().ok_or_else(WebError::internal)?,
        )
        .await?;
    let reply_at = tokio::time::Instant::now() + Duration::from_secs(4);
    let challenge = random_hex(16)?;
    let code = code()?;
    let issued = s.coord("session.email_code_issue", service_actor(), json!({
        "email":email, "challenge":hash(&challenge), "mac":code_mac(&settings.code_key, &challenge, &code)
    })).await?;
    // Give every valid CAPTCHA submission the same response and delivery
    // window, including unknown emails and addresses on cooldown.
    let deliver = async {
        if let Some(recipient) = issued.as_str() {
            settings.send_code(recipient, &code, &challenge).await;
        }
    };
    tokio::join!(deliver, tokio::time::sleep_until(reply_at));
    Ok(Json(json!({"challenge":challenge})))
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Verify {
    challenge: String,
    code: String,
}
fn invalid_code() -> WebError {
    WebError(
        StatusCode::UNAUTHORIZED,
        "Invalid or expired sign-in code".into(),
    )
}
async fn verify(State(s): State<Shared>, ApiJson(b): ApiJson<Verify>) -> WebResult<Response> {
    let settings = s.email_sign_in.as_ref().ok_or_else(unavailable)?;
    if b.challenge.len() != 32
        || !crate::devices::lower_hex(&b.challenge)
        || b.code.len() != 6
        || !b.code.bytes().all(|digit| digit.is_ascii_digit())
    {
        return Err(invalid_code());
    }
    let token = random_hex(32)?;
    let authenticated = s.coord("session.email_code_redeem", service_actor(), json!({
        "challenge":hash(&b.challenge), "mac":code_mac(&settings.code_key, &b.challenge, &b.code), "session":hash(&token)
    })).await?;
    if authenticated == json!(true) {
        Ok(session::cookie(&s, &token, true))
    } else {
        Err(invalid_code())
    }
}

pub(super) fn issue(db: &Db<'_>, input: &Value) -> DbResult<Value> {
    let Some(member) = db.optional(
        "SELECT workspace_id,id,login_email FROM workspace_members
         WHERE lower(login_email)=lower($1) AND role='operator' AND revoked_at IS NULL FOR SHARE",
        &[field(input, "email")?],
    )?
    else {
        return Ok(Value::Null);
    };
    let issued = db.exec(
        "INSERT INTO email_sign_in_codes AS c(workspace_id,member_id,challenge_hash,code_mac,sent_at,send_count)
         VALUES($1,$2,$3,$4,now(),1)
         ON CONFLICT(workspace_id,member_id) DO UPDATE
         SET challenge_hash=excluded.challenge_hash,code_mac=excluded.code_mac,sent_at=now(),
             send_count=CASE WHEN c.sent_at::date=CURRENT_DATE THEN c.send_count+1 ELSE 1 END,
             attempts=0,consumed=false
         WHERE c.sent_at <= now()-interval '60 seconds' AND (c.sent_at::date<CURRENT_DATE OR c.send_count<5)",
        &[member.text(0)?, member.text(1)?, field(input, "challenge")?, field(input, "mac")?],
    )?;
    Ok(if issued == 1 {
        json!(member.text(2)?)
    } else {
        Value::Null
    })
}

pub(super) fn redeem(db: &Db<'_>, input: &Value) -> DbResult<Value> {
    let challenge = field(input, "challenge")?;
    // Lock the member first, matching the lock order for rejecting a revoked
    // member and deleting their challenges and sessions.
    let Some(member) = db.optional(
        "SELECT workspace_id,id FROM workspace_members
         WHERE (workspace_id,id)=(SELECT workspace_id,member_id FROM email_sign_in_codes WHERE challenge_hash=$1)
           AND role='operator' AND login_email IS NOT NULL AND revoked_at IS NULL FOR SHARE",
        &[challenge],
    )? else {
        return Ok(json!(false));
    };
    let (workspace, member_id) = (member.text(0)?, member.text(1)?);
    let Some(code) = db.optional(
        "SELECT code_mac FROM email_sign_in_codes WHERE challenge_hash=$1 AND workspace_id=$2 AND member_id=$3
         AND NOT consumed AND attempts<5 AND sent_at>now()-interval '10 minutes' FOR UPDATE",
        &[challenge, workspace, member_id],
    )? else {
        return Ok(json!(false));
    };
    let matches = bool::from(
        code.text(0)?
            .as_bytes()
            .ct_eq(field(input, "mac")?.as_bytes()),
    );
    db.exec(
        "UPDATE email_sign_in_codes SET attempts=attempts+1,consumed=$2::boolean WHERE challenge_hash=$1",
        &[challenge, if matches { "true" } else { "false" }],
    )?;
    if matches {
        db.exec(
            "INSERT INTO sessions(token_hash,workspace_id,expires_at,member_id)
             VALUES($1,$2,now()+interval '12 hours',$3)",
            &[field(input, "session")?, workspace, member_id],
        )?;
    }
    Ok(json!(matches))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn captcha_requires_a_valid_token_for_the_origin_and_action() {
        let mut response = json!({
            "tokenProperties": {
                "valid":true, "action":"email_sign_in", "hostname":"app.example.test"
            },
            "riskAnalysis":{"score":0.9}
        });
        let accepts = |value| {
            serde_json::from_value::<Assessment>(value)
                .unwrap()
                .accepts("app.example.test")
        };
        assert!(accepts(response.clone()));
        response["tokenProperties"]["hostname"] = json!("other.example.test");
        assert!(!accepts(response.clone()));
        response["tokenProperties"]["hostname"] = json!("app.example.test");
        response["tokenProperties"]["action"] = json!("other");
        assert!(!accepts(response.clone()));
        response["tokenProperties"]["action"] = json!(ACTION);
        response["riskAnalysis"]["score"] = json!(0.3);
        assert!(!accepts(response.clone()));
        response["riskAnalysis"]["score"] = Value::Null;
        assert!(!accepts(response));
    }

    #[test]
    fn email_addresses_and_code_macs_have_a_single_login_identity() {
        assert_eq!(
            address(" Operator+tag@Example.test ").unwrap(),
            "Operator+tag@Example.test"
        );
        assert!(address("Operator <operator@example.test>").is_err());
        assert!(address("operator@example.test,other@example.test").is_err());
        let mut key = [0; 32];
        getrandom::fill(&mut key).unwrap();
        assert_ne!(
            code_mac(&key, "first", "123456"),
            code_mac(&key, "second", "123456")
        );
        assert_ne!(
            code_mac(&key, "first", "123456"),
            code_mac(&key, "first", "654321")
        );
    }
}
