// SPDX-License-Identifier: MPL-2.0
use super::super::{Shared, WebError, WebResult};
use super::{DISCOVERY, Grant, SCOPES};
use axum::{
    Json,
    extract::{Request, State},
    http::{StatusCode, header},
    response::{IntoResponse, Response},
};
use rmcp::{
    ErrorData, RoleServer, ServerHandler,
    model::{
        CallToolRequestParams, CallToolResponse, CallToolResult, ContentBlock, Implementation,
        ListToolsResult, PaginatedRequestParams, ServerCapabilities, ServerConfig, Tool,
    },
    service::RequestContext,
    transport::streamable_http_server::{
        session::never::NeverSessionManager,
        tower::{StreamableHttpServerConfig, StreamableHttpService},
    },
};
use serde_json::{Value, json};
use std::sync::{Arc, LazyLock};

pub(super) async fn discovery(State(s): State<Shared>) -> WebResult<Json<Value>> {
    let oauth = s.integrations.oauth.as_ref().ok_or_else(|| {
        WebError(
            StatusCode::SERVICE_UNAVAILABLE,
            "Configure the Bugsnitch OAuth authorization server".into(),
        )
    })?;
    Ok(Json(
        json!({"resource":format!("{}/mcp",s.origin),"authorization_servers":[oauth.issuer],"scopes_supported":SCOPES}),
    ))
}
pub(super) async fn serve(State(s): State<Shared>, mut req: Request) -> Response {
    let token = req
        .headers()
        .get(header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.strip_prefix("Bearer "))
        .unwrap_or("");
    let grant = s
        .integrations
        .grant(token, &format!("{}/mcp", s.origin))
        .await;
    let Some(grant) = grant.filter(|g| g.scopes.contains("workspace.read")) else {
        return (
            StatusCode::UNAUTHORIZED,
            [(
                header::WWW_AUTHENTICATE,
                format!(
                    "Bearer resource_metadata=\"{}{DISCOVERY}\", scope=\"workspace.read\"",
                    s.origin
                ),
            )],
            Json(json!({"error":"A scoped Bugsnitch plugin grant is required"})),
        )
            .into_response();
    };
    // rmcp makes the HTTP parts available to tool code. Authorize once above
    // and remove credentials before dispatch to the per-request handler.
    req.headers_mut().remove(header::AUTHORIZATION);
    req.headers_mut().remove(header::COOKIE);
    let host = s
        .origin
        .split_once("://")
        .map(|(_, authority)| authority)
        .unwrap_or_default();
    let config = StreamableHttpServerConfig::default()
        .with_legacy_session_mode(false)
        .with_json_response(true)
        .with_allowed_hosts([host])
        .with_allowed_origins([s.origin.clone()])
        .with_max_request_body_bytes(65536);
    let service = StreamableHttpService::new(
        move || {
            Ok(Tools {
                state: s.clone(),
                grant: grant.clone(),
            })
        },
        Arc::new(NeverSessionManager::default()),
        config,
    );
    service.handle(req).await.into_response()
}

// These schemas retain the public contract of the previous release, including
// required fields in nested plan items. Untrusted requests never supply schemas.
static CATALOG: LazyLock<Vec<Tool>> = LazyLock::new(|| {
    serde_json::from_str(include_str!("tools.json")).expect("packaged MCP tool schemas")
});
fn scope(name: &str) -> Option<&'static str> {
    Some(match name {
        "list_projects" | "list_attempts" | "list_decisions" | "get_project_status"
        | "inspect_attempt" => "workspace.read",
        "report_incident" => "incident.write",
        "answer_decision" => "decision.write",
        "submit_feedback" => "feedback.write",
        "request_attempt_control" => "attempt.control",
        "propose_ticket_plan" => "plan.write",
        _ => return None,
    })
}

fn conforms(value: &Value, schema: &Value) -> bool {
    let kind = match value {
        Value::Null => "null",
        Value::String(_) => "string",
        Value::Number(n) if n.is_i64() => "integer",
        Value::Object(_) => "object",
        Value::Array(_) => "array",
        _ => return false,
    };
    let allowed = &schema["type"];
    if allowed != kind
        && !allowed
            .as_array()
            .is_some_and(|kinds| kinds.iter().any(|k| k == kind))
    {
        return false;
    }
    match value {
        Value::Object(o) => {
            let required = schema["required"].as_array().into_iter().flatten();
            required
                .into_iter()
                .all(|k| k.as_str().is_some_and(|key| o.contains_key(key)))
                && o.iter().all(|(k, v)| {
                    schema["properties"]
                        .get(k)
                        .is_some_and(|property| conforms(v, property))
                })
        }
        Value::Array(items) => items.iter().all(|v| conforms(v, &schema["items"])),
        _ => true,
    }
}

#[derive(Clone)]
struct Tools {
    state: Shared,
    grant: Grant,
}
impl Tools {
    fn authorized(&self, tool: &Tool) -> bool {
        scope(&tool.name).is_some_and(|required| self.grant.scopes.contains(required))
    }
}
impl ServerHandler for Tools {
    fn get_info(&self) -> ServerConfig {
        ServerConfig::new(ServerCapabilities::builder().enable_tools().build())
            .with_server_info(Implementation::new("bugsnitch", "0.1.0"))
    }
    async fn list_tools(
        &self,
        _: Option<PaginatedRequestParams>,
        _: RequestContext<RoleServer>,
    ) -> Result<ListToolsResult, ErrorData> {
        Ok(ListToolsResult::with_all_items(
            CATALOG
                .iter()
                .filter(|t| self.authorized(t))
                .cloned()
                .collect(),
        ))
    }
    async fn call_tool(
        &self,
        req: CallToolRequestParams,
        _: RequestContext<RoleServer>,
    ) -> Result<CallToolResponse, ErrorData> {
        let tool = CATALOG
            .iter()
            .find(|t| t.name == req.name && self.authorized(t))
            .ok_or_else(|| ErrorData::invalid_params("Unknown or unauthorized tool", None))?;
        let mut input = Value::Object(req.arguments.unwrap_or_default());
        if !conforms(&input, &Value::Object((*tool.input_schema).clone())) {
            return Err(ErrorData::invalid_params(
                "Arguments do not match the tool schema",
                None,
            ));
        }
        let (private_fields, limit): (&[&str], usize) = match tool.name.as_ref() {
            "report_incident" => (&["observed", "expected", "steps"], 4096),
            "submit_feedback" => (&["body"], 4000),
            _ => (&[], 0),
        };
        for &field in private_fields {
            // Validate the original size before projecting approved report and
            // feedback fields into the private domain call.
            let Some(value) = input[field].as_str().filter(|v| v.len() <= limit) else {
                return Err(ErrorData::invalid_params(
                    "Text exceeds the field limit",
                    None,
                ));
            };
            input[field] = Value::String(crate::redact::redact(value));
        }
        let name = tool.name.as_ref();
        let operation = format!("plugin.{name}");
        let actor = ["plugin".into(), self.grant.id.into()];
        if crate::plugin_reads::is_list(&operation) {
            let list = self.state.pool.raw(&operation, actor, input).await;
            return Ok(match list {
                Ok(bytes) => {
                    // RawValue validated the complete document at the pipe boundary.
                    let text = std::str::from_utf8(&bytes)
                        .map_err(|_| ErrorData::internal_error("Invalid list response", None))?;
                    CallToolResult::success(vec![ContentBlock::text(text.to_owned())])
                }
                Err(e) => CallToolResult::error(vec![ContentBlock::text(e.1)]),
            }
            .into());
        }
        let result = self.state.domain(&operation, actor, input).await;
        let result = match result {
            Ok(value) => CallToolResult::structured(value),
            Err(e) => CallToolResult::error(vec![ContentBlock::text(e.1)]),
        };
        Ok(result.into())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn every_tool_has_a_scope_and_rejects_extra_fields() {
        for tool in CATALOG.iter() {
            assert!(scope(&tool.name).is_some());
            assert!(!conforms(
                &json!({"actor":"owner"}),
                &Value::Object((*tool.input_schema).clone())
            ));
        }
        let t = CATALOG
            .iter()
            .find(|t| t.name == "answer_decision")
            .unwrap();
        let schema = Value::Object((*t.input_schema).clone());
        let mut args =
            json!({"decisionId":"a","answer":"Confirm","requestId":"request_id","version":2});
        assert!(conforms(&args, &schema));
        args["version"] = json!("2");
        assert!(!conforms(&args, &schema));
        args.as_object_mut().unwrap().remove("version");
        assert!(!conforms(&args, &schema));
    }
}
