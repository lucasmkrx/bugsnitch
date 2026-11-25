// SPDX-License-Identifier: MPL-2.0
use super::{lower_hex, valid_sha};
use crate::coord::{Db, Details, Error, Result};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct DeviceCall {
    token_hash: String,
    request: Subject,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Subject {
    protocol: i64,
    attempt_id: String,
    version: i64,
    #[serde(default)]
    request_plan: bool,
    #[serde(default)]
    recipient: String,
    correction: Option<Correction>,
    prepared: Option<Prepared>,
    expires_at: Option<String>,
    #[serde(default)]
    recovery_hold: bool,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Prepared {
    pull_number: i64,
    inspection_version: i64,
    #[serde(rename = "baseSha")]
    base: String,
    independent_review: String,
    combined_validation: String,
}

#[derive(Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Correction {
    root_attempt: String,
    pull_number: i64,
    previous_head: String,
    target: String,
}

fn valid_attempt(id: &str, version: i64) -> bool {
    id.len() == 32 && lower_hex(id) && version > 0
}

pub(super) fn cursor(db: &Db<'_>, input: Value) -> Result<Value> {
    let token = input["tokenHash"].as_str().ok_or_else(Error::denied)?;
    let project = input["request"]["projectId"]
        .as_str()
        .ok_or_else(Error::denied)?;
    let row=db.optional("SELECT coalesce((SELECT max(e.position) FROM events e WHERE (e.workspace_id,e.project_id)=(m.workspace_id,m.project_id)),0)
       FROM devices d JOIN device_projects m ON (m.workspace_id,m.device_id)=(d.workspace_id,d.id)
       WHERE d.token_hash=$1 AND NOT d.revoked AND m.project_id=$2",&[&token,&project])?.ok_or_else(Error::denied)?;
    Ok(json!({"position":row.i64(0)?}))
}

pub(super) fn device(db: &Db<'_>, input: Value) -> Result<Value> {
    let DeviceCall {
        token_hash,
        request: b,
    } = serde_json::from_value(input)?;
    if b.protocol != 1 || !valid_attempt(&b.attempt_id, b.version) || b.recipient.len() > 100 {
        return Err(Error::bad("Invalid attempt progression request"));
    }
    let revision = b.version.to_string();
    let identity = db.optional(
        "SELECT a.workspace_id,a.project_id,a.device_id,w.account_id,p.policy_version,
          coalesce((SELECT id FROM plan_versions v WHERE (v.workspace_id,v.project_id)=(a.workspace_id,a.project_id)
              AND v.approved_at IS NOT NULL ORDER BY v.version DESC LIMIT 1),''),
          a.state='waiting' AND a.phase='implement' AND a.reason_code='plan_review_required'
            AND NOT p.paused AND NOT w.stopped AND EXISTS (
               SELECT 1 FROM admission ad WHERE (ad.workspace_id,ad.project_id,ad.device_id,ad.attempt_id)
                 =(a.workspace_id,a.project_id,a.device_id,a.id) AND ad.owns_issue
          ),a.state='completed' AND a.phase='wrap_up' AND NOT p.paused AND NOT w.stopped AND p.allow_draft_pr,
          a.issue_number,a.candidate_sha
         FROM attempt_reports a JOIN devices d ON (d.workspace_id,d.id)=(a.workspace_id,a.device_id)
         JOIN device_projects dp ON (dp.workspace_id,dp.project_id,dp.device_id)=(a.workspace_id,a.project_id,a.device_id)
         JOIN projects p ON (p.workspace_id,p.id)=(a.workspace_id,a.project_id)
         JOIN workspaces w ON w.id=a.workspace_id
         WHERE d.token_hash=$1 AND NOT d.revoked AND a.id=$2 AND a.version=$3
         FOR SHARE OF w,p,d,dp,a",
        &[&token_hash,&b.attempt_id,&revision],
    )?.ok_or_else(Error::denied)?;
    let (ws, project, device) = (identity.text(0)?, identity.text(1)?, identity.text(2)?);
    if b.request_plan || b.correction.is_some() {
        let expiry = b.expires_at.as_deref().unwrap_or("");
        if !expiry.is_empty()
            && OffsetDateTime::parse(expiry, &Rfc3339)
                .ok()
                .is_none_or(|e| e <= OffsetDateTime::now_utc())
        {
            return Err(Error::conflict("The local authorization expired"));
        }
        if identity.text(5)?.is_empty()
            || (b.request_plan && (!identity.bool(6)? || b.correction.is_some()))
        {
            return Err(Error::conflict(
                "The plan checkpoint is no longer waiting for approval",
            ));
        }
        let (kind, subject, reason) = if let Some(c) = b.correction {
            if !valid_attempt(&c.root_attempt, 1)
                || c.root_attempt == b.attempt_id
                || c.pull_number < 1
                || c.target.is_empty()
                || c.target.len() > 160
                || !valid_sha(&c.previous_head)
                || c.previous_head == identity.text(9)?
                || !identity.bool(7)?
            {
                return Err(Error::conflict(
                    "The correction no longer matches the observed pull request",
                ));
            }
            let observed=db.optional(
                    "SELECT o.snapshot->>'base' FROM draft_pr_intents x
                     JOIN attempt_reports a ON (a.workspace_id,a.project_id,a.id)=(x.workspace_id,x.project_id,x.attempt_id)
                     JOIN pull_observations o ON (o.workspace_id,o.project_id,o.number)=(x.workspace_id,x.project_id,x.number)
                     WHERE x.workspace_id=$1 AND x.project_id=$2 AND a.device_id=$3 AND a.issue_number=$4
                       AND x.attempt_id=$5 AND x.number=$6 AND x.target=$7 AND x.status='created' AND x.local_ship
                       AND o.snapshot->>'head'=$8 AND o.snapshot->>'state'='OPEN' AND o.snapshot->>'target'=$7
                       AND o.snapshot->>'review' IS DISTINCT FROM 'APPROVED' AND o.snapshot->'queue' IS NULL",
                    &[ws,project,device,identity.text(8)?,&c.root_attempt,&c.pull_number.to_string(),&c.target,&c.previous_head],
                )?.ok_or_else(||Error::conflict("The correction no longer matches the observed pull request"))?;
            let base = observed.text(0)?;
            if !valid_sha(base) {
                return Err(Error::conflict(
                    "The correction lacks an inspected target revision",
                ));
            }
            let mut subject = serde_json::to_value(c)?;
            subject["baseSha"] = json!(base);
            (
                "publish_correction",
                subject.to_string(),
                "Approval requested for the exact reviewed PR correction",
            )
        } else {
            (
                "implementation_plan",
                "{}".to_owned(),
                "Approval requested for the exact local implementation plan",
            )
        };
        let recipient = if b.recipient.is_empty() || kind == "publish_correction" {
            identity.text(3)?
        } else {
            &b.recipient
        };
        if recipient != identity.text(3)?
            && !db
                .one(
                    "SELECT EXISTS(SELECT 1 FROM workspace_members WHERE workspace_id=$1 AND id=$2
             AND role='approver' AND revoked_at IS NULL AND (project_id IS NULL OR project_id=$3))",
                    &[ws, recipient, project],
                )?
                .bool(0)?
        {
            return Err(Error::denied());
        }
        let inserted = db.optional(
            "INSERT INTO ticket_decisions(workspace_id,attempt_id,attempt_version,kind,subject,recipient,subject_policy,plan_id,expires_at)
             VALUES($1,$2,$3,$7,$8::jsonb,$4,$5,$6,least(now()+interval '24 hours',nullif($9,'')::timestamptz)) ON CONFLICT DO NOTHING RETURNING attempt_id",
            &[ws,&b.attempt_id,&revision,recipient,identity.text(4)?,identity.text(5)?,kind,&subject,expiry],
        )?;
        if inserted.is_some() {
            db.event(
                ws,
                project,
                "ticket_decision.requested",
                &b.attempt_id,
                ("device", device),
                Details {
                    version: b.version,
                    reason,
                    ..Default::default()
                },
            )?;
        }
    }
    if let Some(e) = b.prepared {
        if e.pull_number <= 0
            || e.inspection_version <= 0
            || !valid_sha(&e.base)
            || [&e.independent_review, &e.combined_validation]
                .iter()
                .any(|d| d.len() != 64 || !lower_hex(d))
        {
            return Err(Error::bad("Invalid prepared sign-off evidence"));
        }
        let pull = e.pull_number.to_string();
        let version = e.inspection_version.to_string();
        let inserted=db.optional(
            "INSERT INTO prepared_signoffs(workspace_id,attempt_id,attempt_version,pull_number,inspection_version,candidate_sha,base_sha,independent_review,combined_validation)
             SELECT s.workspace_id,s.id,s.version,o.number,o.version,o.snapshot->>'head',$6,$7,$8
             FROM attempt_progression s JOIN pull_observations o ON (o.workspace_id,o.project_id,o.number)=(s.workspace_id,s.project_id,$4)
             WHERE s.workspace_id=$1 AND s.id=$2 AND s.version=$3 AND s.action->>'kind'='prepare_signoff'
               AND s.action->'pull'->>'number'=$4::text AND o.version=$5
               AND o.snapshot->>'head'=s.action->>'candidateSha' AND o.snapshot->>'base'=$6 AND o.snapshot->>'checks'='SUCCESS'
             ON CONFLICT DO NOTHING RETURNING attempt_id",
            &[ws,&b.attempt_id,&revision,&pull,&version,&e.base,&e.independent_review,&e.combined_validation],
        )?;
        if inserted.is_some() {
            db.event(
                ws,
                project,
                "pull.sign_off_evidence_prepared",
                &b.attempt_id,
                ("device", device),
                Details {
                    version: b.version,
                    reason: "Device prepared independent review and combined validation references",
                    ..Default::default()
                },
            )?;
        }
    }
    if b.recovery_hold {
        let inserted=db.optional("INSERT INTO attempt_recovery_holds(workspace_id,attempt_id,attempt_version)
            SELECT workspace_id,id,version FROM attempt_reports WHERE workspace_id=$1 AND id=$2 AND version=$3 AND state='running'
            ON CONFLICT DO NOTHING RETURNING attempt_id",&[ws,&b.attempt_id,&revision])?;
        if inserted.is_some() {
            db.event(
                ws,
                project,
                "attempt.recovery_held",
                &b.attempt_id,
                ("device", device),
                Details {
                    version: b.version,
                    reason: "Execution needs inspection on the owning runner",
                    ..Default::default()
                },
            )?;
        }
    }
    let row = db.one("SELECT action::text FROM attempt_progression WHERE workspace_id=$1 AND id=$2 AND version=$3",
        &[ws,&b.attempt_id,&revision])?;
    Ok(serde_json::from_str(row.text(0)?)?)
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Answer {
    workspace: String,
    attempt_id: String,
    version: i64,
    answer: String,
}

pub(super) fn answer(db: &Db<'_>, b: Answer) -> Result<Value> {
    if !valid_attempt(&b.attempt_id, b.version) || !matches!(b.answer.as_str(), "approve" | "hold")
    {
        return Err(Error::bad(
            "Choose Approve or Hold for the exact checkpoint",
        ));
    }
    let revision = b.version.to_string();
    let (role, actor) = db.actor();
    let row = db.optional(
        "SELECT a.project_id,q.answer,q.kind FROM ticket_decisions q
         JOIN attempt_reports a ON (a.workspace_id,a.id,a.version)=(q.workspace_id,q.attempt_id,q.attempt_version)
         JOIN projects p ON (p.workspace_id,p.id)=(a.workspace_id,a.project_id)
         JOIN workspaces w ON w.id=a.workspace_id
         JOIN devices d ON (d.workspace_id,d.id)=(a.workspace_id,a.device_id)
         WHERE q.workspace_id=$1 AND q.attempt_id=$2 AND q.attempt_version=$3 AND q.recipient=$4
           AND q.expires_at>now() AND q.subject_policy=p.policy_version
           AND q.plan_id=(SELECT id FROM plan_versions v WHERE (v.workspace_id,v.project_id)=(a.workspace_id,a.project_id)
                 AND v.approved_at IS NOT NULL ORDER BY v.version DESC LIMIT 1)
           AND ((q.kind='implementation_plan' AND a.state='waiting' AND a.phase='implement' AND a.reason_code='plan_review_required')
             OR (q.kind='publish_correction' AND a.state='completed' AND a.phase='wrap_up' AND p.allow_draft_pr
               AND EXISTS(SELECT 1 FROM pull_observations o
                 WHERE (o.workspace_id,o.project_id,o.number)=(a.workspace_id,a.project_id,(q.subject->>'pullNumber')::bigint)
                  AND o.snapshot->>'head'=q.subject->>'previousHead' AND o.snapshot->>'target'=q.subject->>'target'
                  AND o.snapshot->>'base'=q.subject->>'baseSha'
                  AND o.snapshot->>'state'='OPEN' AND o.snapshot->>'review' IS DISTINCT FROM 'APPROVED'
                  AND o.snapshot->'queue' IS NULL)))
           AND NOT p.paused AND NOT w.stopped AND NOT d.revoked
           AND (($5='owner' AND w.account_id=$4) OR ($5='approver' AND EXISTS(
             SELECT 1 FROM workspace_members m WHERE m.workspace_id=w.id AND m.id=$4
               AND m.role='approver' AND m.revoked_at IS NULL AND (m.project_id IS NULL OR m.project_id=p.id)
           ))) FOR UPDATE OF q FOR SHARE OF a,p,w,d",
        &[&b.workspace,&b.attempt_id,&revision,actor,role],
    )?.ok_or_else(|| Error::conflict("The ticket decision is stale or assigned to another approver"))?;
    if let Some(previous) = row.opt(1)? {
        return if previous == b.answer {
            Ok(json!({"answer":b.answer}))
        } else {
            Err(Error::conflict("This checkpoint already has an answer"))
        };
    }
    db.exec(
        "UPDATE ticket_decisions SET answer=$4,answered_at=now()
         WHERE workspace_id=$1 AND attempt_id=$2 AND attempt_version=$3 AND kind=$5",
        &[
            &b.workspace,
            &b.attempt_id,
            &revision,
            &b.answer,
            row.text(2)?,
        ],
    )?;
    db.event(
        &b.workspace,
        row.text(0)?,
        "ticket_decision.answered",
        &b.attempt_id,
        db.actor(),
        Details {
            version: b.version,
            reason: &b.answer,
            ..Default::default()
        },
    )?;
    Ok(json!({"answer":b.answer}))
}
