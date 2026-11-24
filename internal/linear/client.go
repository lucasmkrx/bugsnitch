// SPDX-License-Identifier: MPL-2.0
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	Unavailable = errors.New("Linear issue could not be read")
	identifier  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,39}-[0-9]{1,12}$`)
	uuid        = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)
)

type Issue struct {
	ID, Identifier, Title, URL string
	State                      struct{ Name, Type string }
	UpdatedAt                  time.Time
}

type Client struct {
	apiKey string
	http   *http.Client
}

func New(apiKey string) *Client {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	return &Client{apiKey: apiKey, http: &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func ValidUUID(value string) bool { return uuid.MatchString(value) }

func ValidIssueID(value string) bool { return identifier.MatchString(value) || uuid.MatchString(value) }

func (c *Client) Issue(ctx context.Context, id string) (Issue, error) {
	var empty Issue
	if !ValidIssueID(id) || c == nil {
		return empty, Unavailable
	}
	payload, err := json.Marshal(map[string]any{
		"query":     `query ReadIssue($id: ID!) { issue(id: $id) { id identifier title url updatedAt state { name type } } }`,
		"variables": map[string]string{"id": id},
	})
	if err != nil {
		return empty, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.linear.app/graphql", bytes.NewReader(payload))
	if err != nil {
		return empty, err
	}
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return empty, Unavailable
	}
	defer resp.Body.Close()
	var reply struct {
		Data   struct{ Issue *Issue }
		Errors []json.RawMessage
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK || dec.Decode(&reply) != nil || dec.Decode(new(any)) != io.EOF || len(reply.Errors) != 0 || reply.Data.Issue == nil {
		return empty, Unavailable
	}
	issue := *reply.Data.Issue
	if !uuid.MatchString(issue.ID) || !identifier.MatchString(issue.Identifier) ||
		(!strings.EqualFold(id, issue.ID) && !strings.EqualFold(id, issue.Identifier)) ||
		issue.UpdatedAt.IsZero() || issue.Title == "" || issue.State.Name == "" || issue.State.Type == "" {
		return empty, Unavailable
	}
	link, err := url.Parse(issue.URL)
	if err != nil || link.Scheme != "https" || link.Host != "linear.app" || link.User != nil ||
		link.RawQuery != "" || link.Fragment != "" || !strings.Contains(link.Path, "/issue/"+issue.Identifier) || len(issue.URL) > 1024 {
		return empty, Unavailable
	}
	return issue, nil
}
