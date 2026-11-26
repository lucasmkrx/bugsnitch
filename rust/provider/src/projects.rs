// SPDX-License-Identifier: MPL-2.0
use crate::{
    client::{Error, Result},
    repo::Repository,
};
use serde::Deserialize;
use serde_json::{Value, json};

const ITEM_FIELDS: &str = r#"fragment ItemFields on ProjectV2Item {
  id isArchived
  fieldValueByName(name: "Status") { ... on ProjectV2ItemFieldSingleSelectValue { name } }
  content { __typename ... on Issue { id number state title repository { id nameWithOwner } } }
}"#;
const LIST: &str = r#"query ProjectIssues($owner:String!, $number:Int!, $after:String) {
  organization(login:$owner) { projectV2(number:$number) {
    number title closed items(first:50, after:$after) { nodes { ...ItemFields } pageInfo { hasNextPage endCursor } }
  } }
}"#;
const IMPORT: &str = r#"query ProjectImport($item:ID!) {
  node(id:$item) { ... on ProjectV2Item { ...ItemFields project {
    id number closed owner { __typename ... on Organization { login } }
  } } }
}"#;

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Item {
    id: String,
    is_archived: bool,
    content: Option<Value>,
    field_value_by_name: Option<Value>,
}

struct Candidate<'a> {
    node: &'a str,
    number: i64,
    title: &'a str,
    status: &'a str,
}
impl Item {
    fn candidate(&self, repository: &Repository) -> Option<Candidate<'_>> {
        let c = self.content.as_ref()?;
        if self.is_archived
            || !opaque(&self.id, 256)
            || c["__typename"] != "Issue"
            || c["state"] != "OPEN"
            || c["repository"]["id"] != repository.node_id
            || !c["repository"]["nameWithOwner"]
                .as_str()?
                .eq_ignore_ascii_case(&repository.name)
        {
            return None;
        }
        let node = c["id"].as_str()?;
        let number = c["number"].as_i64()?;
        if !opaque(node, 256) || number < 1 {
            return None;
        }
        Some(Candidate {
            node,
            number,
            title: c["title"].as_str()?,
            status: self
                .field_value_by_name
                .as_ref()
                .and_then(|v| v["name"].as_str())
                .unwrap_or_default(),
        })
    }
}

fn opaque(s: &str, limit: usize) -> bool {
    !s.is_empty() && s.len() <= limit && s.bytes().all(|b| b.is_ascii_graphic())
}

impl Repository {
    fn project_query(&self, operation: &str, variables: Value) -> Result<Value> {
        self.project_graphql(&format!("{operation}\n{ITEM_FIELDS}"), variables)
    }

    fn project_graphql(&self, query: &str, variables: Value) -> Result<Value> {
        let request = json!({"query":query, "variables":variables});
        let reply: Value = self
            .client
            .request(&self.token, "/graphql", Some(&request), 200)?;
        if !reply["errors"].is_null() || !reply["data"].is_object() {
            return Err(Error::Response);
        }
        Ok(reply["data"].clone())
    }

    pub fn project_issues(&self, board: i32, after: Option<&str>) -> Result<Value> {
        if board < 1 || after.is_some_and(|s| !opaque(s, 1024)) {
            return Err(Error::Invalid);
        }
        let (owner, _) = self.name.split_once('/').ok_or(Error::Invalid)?;
        let data = self.project_query(LIST, json!({"owner":owner,"number":board,"after":after}))?;
        let project = &data["organization"]["projectV2"];
        if project["closed"] != false || project["number"] != board {
            return Err(Error::SubjectChanged);
        }
        let title = project["title"].as_str().ok_or(Error::Response)?;
        let items = &project["items"];
        let mut issues = vec![];
        for raw in items["nodes"].as_array().ok_or(Error::Response)? {
            if raw.is_null() {
                continue;
            }
            let item: Item = serde_json::from_value(raw.clone()).map_err(|_| Error::Response)?;
            if let Some(c) = item.candidate(self) {
                issues.push(
                    json!({"number":c.number,"title":c.title,"status":c.status,"itemId":item.id}),
                );
            }
        }
        let page = &items["pageInfo"];
        let next = if page["hasNextPage"].as_bool().ok_or(Error::Response)? {
            Some(
                page["endCursor"]
                    .as_str()
                    .filter(|s| opaque(s, 1024))
                    .ok_or(Error::Response)?,
            )
        } else {
            None
        };
        Ok(json!({"number":board,"title":title,"issues":issues,"nextCursor":next}))
    }

    pub fn project_import(&self, board: i32, issue: i32, item_id: &str) -> Result<Value> {
        if board < 1 || issue < 1 || !opaque(item_id, 256) {
            return Err(Error::Invalid);
        }
        let data = self.project_query(IMPORT, json!({"item":item_id}))?;
        let node = &data["node"];
        let project = &node["project"];
        let (owner, _) = self.name.split_once('/').ok_or(Error::Invalid)?;
        if project["closed"] != false
            || project["number"] != board
            || project["owner"]["__typename"] != "Organization"
            || !project["owner"]["login"]
                .as_str()
                .is_some_and(|s| s.eq_ignore_ascii_case(owner))
        {
            return Err(Error::SubjectChanged);
        }
        let project_id = project["id"]
            .as_str()
            .filter(|s| opaque(s, 256))
            .ok_or(Error::Response)?;
        let item: Item = serde_json::from_value(node.clone()).map_err(|_| Error::Response)?;
        let c = item.candidate(self).ok_or(Error::SubjectChanged)?;
        if item.id != item_id || c.number != i64::from(issue) {
            return Err(Error::SubjectChanged);
        }
        let canonical = self.get(c.number)?;
        if canonical.node_id.as_deref() != Some(c.node) || canonical.state != "open" {
            return Err(Error::SubjectChanged);
        }
        Ok(json!({"projectNode":project_id,"itemId":item.id,"status":c.status,"issue":canonical}))
    }
}

const STATUS_SUBJECT: &str = r#"query ProjectStatusSubject($item:ID!) {
  node(id:$item) { ... on ProjectV2Item {
    id isArchived
    content { __typename ... on Issue { id number repository { id nameWithOwner } } }
    fieldValueByName(name:"Status") { ... on ProjectV2ItemFieldSingleSelectValue { optionId } }
    project { id closed owner { __typename ... on Organization { login } }
      field(name:"Status") { ... on ProjectV2SingleSelectField { id options { id name } } }
    }
  } }
}"#;
const SET_STATUS: &str = r#"mutation ProjectStatus($project:ID!, $item:ID!, $field:ID!, $option:String!) {
  updateProjectV2ItemFieldValue(input:{projectId:$project,itemId:$item,fieldId:$field,value:{singleSelectOptionId:$option}}) {
    projectV2Item { id }
  }
}"#;

impl Repository {
    pub fn project_status(
        &self,
        issue: i64,
        github_id: i64,
        project_node: &str,
        item_node: &str,
        status: &str,
    ) -> Result<()> {
        if issue < 1
            || github_id < 1
            || !opaque(project_node, 256)
            || !opaque(item_node, 256)
            || status.trim().is_empty()
            || status.len() > 256
            || status.chars().any(char::is_control)
        {
            return Err(Error::Invalid);
        }
        let (owner, _) = self.name.split_once('/').ok_or(Error::Invalid)?;
        let data = self.project_graphql(STATUS_SUBJECT, json!({"item":item_node}))?;
        let item = &data["node"];
        let project = &item["project"];
        let content = &item["content"];
        if item["id"] != item_node
            || item["isArchived"] != false
            || project["closed"] != false
            || project["id"] != project_node
            || project["owner"]["__typename"] != "Organization"
            || !project["owner"]["login"]
                .as_str()
                .is_some_and(|s| s.eq_ignore_ascii_case(owner))
            || content["__typename"] != "Issue"
            || content["number"] != issue
            || content["repository"]["id"] != self.node_id
            || !content["repository"]["nameWithOwner"]
                .as_str()
                .is_some_and(|s| s.eq_ignore_ascii_case(&self.name))
        {
            return Err(Error::SubjectChanged);
        }
        let canonical = self.get(issue)?;
        let node_id = content["id"]
            .as_str()
            .filter(|id| opaque(id, 256))
            .ok_or(Error::SubjectChanged)?;
        if canonical.id != github_id || canonical.node_id.as_deref() != Some(node_id) {
            return Err(Error::SubjectChanged);
        }
        let field = &project["field"];
        let field_id = field["id"]
            .as_str()
            .filter(|s| opaque(s, 256))
            .ok_or(Error::Configuration)?;
        let options = field["options"].as_array().ok_or(Error::Configuration)?;
        let mut matching = options.iter().filter(|o| {
            o["name"]
                .as_str()
                .is_some_and(|s| s.eq_ignore_ascii_case(status))
        });
        let option = matching
            .next()
            .and_then(|o| o["id"].as_str())
            .filter(|id| opaque(id, 256))
            .ok_or(Error::Configuration)?;
        if matching.next().is_some() {
            return Err(Error::Configuration);
        }
        if item["fieldValueByName"]["optionId"] == option {
            return Ok(());
        }
        let reply = self.project_graphql(
            SET_STATUS,
            json!({"project":project_node,"item":item_node,"field":field_id,"option":option}),
        )?;
        if reply["updateProjectV2ItemFieldValue"]["projectV2Item"]["id"] != item_node {
            return Err(Error::Response);
        }
        Ok(())
    }
}
