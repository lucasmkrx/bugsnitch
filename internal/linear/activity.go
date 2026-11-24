// SPDX-License-Identifier: MPL-2.0
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// Publish a stable activity id. A duplicate mutation may return an error even
// after a successful first write. Read that same id to reconcile a dropped reply.
func (c *Client) Activity(ctx context.Context, session, id, kind, body string) error {
	if !ValidUUID(session) || !ValidUUID(id) || (kind != "thought" && kind != "response") || len(body) > 4000 {
		return Unavailable
	}
	var write struct {
		Create struct {
			Success  bool
			Activity struct{ ID string } `json:"agentActivity"`
		} `json:"agentActivityCreate"`
	}
	input := map[string]any{"id": id, "agentSessionId": session, "content": map[string]string{"type": kind, "body": body}}
	if c.activityQuery(ctx, `mutation Publish($input: AgentActivityCreateInput!){agentActivityCreate(input:$input){success agentActivity{id}}}`, map[string]any{"input": input}, &write) == nil && write.Create.Success && write.Create.Activity.ID == id {
		return nil
	}
	var read struct {
		Activity struct {
			ID      string
			Session struct{ ID string } `json:"agentSession"`
			Content struct{ Body, Type string }
		} `json:"agentActivity"`
	}
	if err := c.activityQuery(ctx, `query Receipt($id:String!){agentActivity(id:$id){id agentSession{id} content{... on AgentActivityThoughtContent{body type} ... on AgentActivityResponseContent{body type}}}}`, map[string]any{"id": id}, &read); err != nil {
		return err
	}
	if read.Activity.ID != id || read.Activity.Session.ID != session || read.Activity.Content.Body != body || read.Activity.Content.Type != kind {
		return Unavailable
	}
	return nil
}
func (c *Client) activityQuery(ctx context.Context, query string, variables map[string]any, result any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.linear.app/graphql", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	reply, err := c.http.Do(req)
	if err != nil {
		return Unavailable
	}
	defer reply.Body.Close()
	var out struct {
		Data   json.RawMessage
		Errors []json.RawMessage
	}
	d := json.NewDecoder(io.LimitReader(reply.Body, 64<<10))
	if reply.StatusCode != 200 || d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF || len(out.Errors) > 0 || len(out.Data) == 0 || json.Unmarshal(out.Data, result) != nil {
		return Unavailable
	}
	return nil
}
