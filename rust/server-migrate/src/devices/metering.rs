// SPDX-License-Identifier: MPL-2.0
use super::valid_key;
use crate::coord::{Db, Error, Result};
use serde::Deserialize;
use serde_json::Value;

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct Reading {
    subject: String,
    sequence: i64,
    #[serde(default)]
    general: bool,
    model: String,
    input: i64,
    output: i64,
}
impl Reading {
    pub(super) fn record(
        &self,
        db: &Db<'_>,
        ws: &str,
        device: &str,
        projects: &[Value],
    ) -> Result<()> {
        if !valid_key(&self.subject)
            || self.sequence < 1
            || self.general && self.sequence != 1
            || self.model.len() > 120
            || !self
                .model
                .as_bytes()
                .first()
                .is_some_and(u8::is_ascii_alphanumeric)
            || !self
                .model
                .bytes()
                .all(|c| c.is_ascii_alphanumeric() || matches!(c, b'.' | b':' | b'_' | b'-'))
            || !crate::redact::shareable(&self.model)
            || !(0..=1 << 32).contains(&self.input)
            || !(0..=1 << 32).contains(&self.output)
        {
            return Err(Error::bad("Invalid metering record"));
        }
        let kind = if self.general {
            "local_model.started"
        } else {
            "admission.turn_start"
        };
        let event=db.optional("SELECT seq,coalesce(project_id,'') FROM events WHERE workspace_id=$1 AND actor_source='device' AND actor_id=$2
            AND kind=$3 AND subject_id=$4 AND subject_version=$5", &[ws,device,kind,&self.subject,&self.sequence.to_string()])?.ok_or_else(Error::denied)?;
        let project = event.text(1)?;
        if !project.is_empty() && !projects.iter().any(|p| p["id"] == project) {
            return Err(Error::denied());
        }
        let args = [
            event.text(0)?,
            &self.model,
            &self.input.to_string(),
            &self.output.to_string(),
        ];
        if db.exec("INSERT INTO model_turn_metering(event_seq,model,input_tokens,output_tokens) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING",&args)?==0 &&
            !db.one("SELECT (model,input_tokens,output_tokens)=($2,$3::bigint,$4::bigint) FROM model_turn_metering WHERE event_seq=$1",&args)?.bool(0)? {
            return Err(Error::conflict("Usage already recorded for this turn"))
        }
        Ok(())
    }
}
