// SPDX-License-Identifier: MPL-2.0
use super::{
    ApiJson, Device, Extension, Json, Owner, Reply, Shared, State, compatible, device_actor,
};
use crate::http::{WebError, WebResult, service_actor};
use axum::http::StatusCode;
use hmac::{Hmac, KeyInit, Mac};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use sha2::Sha256;
use subtle::ConstantTimeEq;
use time::OffsetDateTime;

const SESSION_SECONDS: i64 = 5 * 60;

#[derive(Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(super) struct Grant {
    device_id: String,
    expiry: i64,
    proof: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct DeviceRequest {
    protocol: i64,
    digest: String,
    permit: Option<Grant>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct OwnerRequest {
    digest: String,
    claim: Grant,
}

fn denied() -> WebError {
    WebError(
        StatusCode::FORBIDDEN,
        "Runner connection could not be verified".into(),
    )
}

fn valid_digest(digest: &str) -> WebResult<()> {
    if digest.len() != 64 || !crate::devices::lower_hex(digest) {
        return Err(denied());
    }
    Ok(())
}

fn sign(key: &str, purpose: &str, workspace: &str, digest: &str, grant: &Grant) -> String {
    let mut mac = Hmac::<Sha256>::new_from_slice(key.as_bytes()).expect("HMAC accepts this key");
    mac.update(b"bugsnitch:local-model-settings:v1\0");
    let subject = (
        &purpose,
        &workspace,
        &digest,
        &grant.device_id,
        grant.expiry,
    );
    mac.update(&serde_json::to_vec(&subject).expect("scalar subject"));
    hex::encode(mac.finalize().into_bytes())
}

fn verify(
    key: &str,
    purpose: &str,
    workspace: &str,
    digest: &str,
    grant: &Grant,
    now: i64,
) -> WebResult<()> {
    let expected = sign(key, purpose, workspace, digest, grant);
    if grant.expiry <= now
        || grant.expiry > now + SESSION_SECONDS
        || !bool::from(expected.as_bytes().ct_eq(grant.proof.as_bytes()))
    {
        return Err(denied());
    }
    Ok(())
}

pub(super) async fn device(
    State(s): State<Shared>,
    Device(credential): Device,
    ApiJson(input): ApiJson<DeviceRequest>,
) -> Reply {
    compatible(input.protocol)?;
    valid_digest(&input.digest)?;
    let identity = s
        .coord(
            "device.model_settings_identity",
            device_actor(),
            Value::String(credential),
        )
        .await?;
    let workspace = identity["workspace"].as_str().ok_or_else(denied)?;
    let device = identity["device"].as_str().ok_or_else(denied)?;
    let now = OffsetDateTime::now_utc().unix_timestamp();
    if let Some(permit) = input.permit {
        if permit.device_id != device {
            return Err(denied());
        }
        verify(
            &s.owner_hash,
            "owner",
            workspace,
            &input.digest,
            &permit,
            now,
        )?;
        return Ok(Json(json!({"authorized":true})));
    }
    let mut claim = Grant {
        device_id: device.into(),
        expiry: now + SESSION_SECONDS,
        proof: String::new(),
    };
    claim.proof = sign(&s.owner_hash, "runner", workspace, &input.digest, &claim);
    Ok(Json(json!({"claim":claim})))
}

pub(super) async fn authorize(
    State(s): State<Shared>,
    Extension(owner): Extension<Owner>,
    ApiJson(input): ApiJson<OwnerRequest>,
) -> Reply {
    valid_digest(&input.digest)?;
    let mut permit = input.claim;
    verify(
        &s.owner_hash,
        "runner",
        &owner.workspace,
        &input.digest,
        &permit,
        OffsetDateTime::now_utc().unix_timestamp(),
    )?;
    s.coord(
        "device.model_settings_active",
        service_actor(),
        json!({
            "workspace":owner.workspace, "device":permit.device_id
        }),
    )
    .await?;
    permit.proof = sign(
        &s.owner_hash,
        "owner",
        &owner.workspace,
        &input.digest,
        &permit,
    );
    Ok(Json(json!({"permit":permit})))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn proof_binds_runner_owner_workspace_and_expiry() {
        let mut grant = Grant {
            device_id: "device-a".into(),
            expiry: 220,
            proof: String::new(),
        };
        grant.proof = sign("secret", "runner", "first", "digest", &grant);
        assert!(verify("secret", "runner", "first", "digest", &grant, 200).is_ok());
        for (purpose, workspace, digest, now) in [
            ("owner", "first", "digest", 200),
            ("runner", "second", "digest", 200),
            ("runner", "first", "other", 200),
            ("runner", "first", "digest", 220),
        ] {
            assert!(verify("secret", purpose, workspace, digest, &grant, now).is_err());
        }
        grant.device_id = "device-b".into();
        assert!(verify("secret", "runner", "first", "digest", &grant, 200).is_err());
    }
}
