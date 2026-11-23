// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/github"
)

func verificationIssueJSON(t *testing.T, record *githubIssueRecord) []byte {
	t.Helper()
	// The issues endpoint omits pull_request entirely for ordinary issues.
	data, err := json.Marshal(map[string]any{"id": record.ID, "number": record.Number, "updated_at": record.UpdatedAt, "html_url": record.URL, "title": record.Title, "state": record.State, "body": record.Body})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func verificationFixture(t *testing.T) (Issue, *githubIssueRecord) {
	t.Helper()
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	t.Setenv("BUGSNITCH_GITHUB_READ_TOKEN", "test-read-token")
	selected := Issue{Repository: "fixture/notes", Number: 42, Criteria: []string{"Co-teacher can reopen the note", "Adjacent week is empty"}}
	record := &githubIssueRecord{Issue: github.Issue{ID: 5042, Number: 42, UpdatedAt: "2026-11-23T12:02:30Z", Title: "Room Week Notes", Body: "Reviewed scope", State: "open"}, URL: "https://github.com/fixture/notes/issues/42"}
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = offlineGitHub(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.String() != "https://api.github.com/repos/fixture/notes/issues/42" {
			t.Fatal("unexpected GitHub operation", r.Method, r.URL)
		}
		data := verificationIssueJSON(t, record)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Request: r}, nil
	})
	return selected, record
}

func TestVerificationReceiptCoversCriteriaWithoutImplementation(t *testing.T) {
	issue, githubIssue := verificationFixture(t)
	input := VerificationInput{Environment: "staging", Revision: "notes-00037-tan", Source: "operator", Observer: "internal tester", Observations: []CriterionObservation{
		{issue.Criteria[1], "passed", "The same room showed an empty note in the following week."},
		{issue.Criteria[0], "passed", "A second teacher reopened the saved note in the selected room."},
	}}
	receipt, err := RecordVerification(t.Context(), issue, input, nil)
	if err != nil || receipt.Result != "passed" || receipt.Issue.GitHubID != 5042 || receipt.Issue.RequirementsDigest != githubIssue.RequirementsDigest() || receipt.RecordedAt == "" {
		t.Fatal(receipt, err)
	}
	if receipt.Observations[0].Criterion != issue.Criteria[0] || receipt.Observations[1].Criterion != issue.Criteria[1] {
		t.Fatal("receipt lost the reviewed criterion order")
	}
	replay, err := RecordVerification(t.Context(), receipt.Issue, input, nil)
	if err != nil || !reflect.DeepEqual(receipt, replay) {
		t.Fatal("same observations created a different receipt", replay, err)
	}
	saved, err := GetVerification(receipt.ID)
	if err != nil || !reflect.DeepEqual(saved, receipt) {
		t.Fatal("receipt not retrievable", saved, err)
	}
	if attempts, err := Summaries(); err != nil || len(attempts) != 0 {
		t.Fatal("verification created an implementation attempt", attempts, err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("BUGSNITCH_HOME"), "worktrees")); !os.IsNotExist(err) {
		t.Fatalf("verification created a checkout: %v", err)
	}
	githubIssue.Body += " changed"
	if _, err := RecordVerification(t.Context(), receipt.Issue, input, nil); err == nil {
		t.Fatal("accepted stale canonical issue")
	}
}

func TestVerificationRejectsUnsupportedClaims(t *testing.T) {
	issue, _ := verificationFixture(t)
	a, b := issue.Criteria[0], issue.Criteria[1]
	passed := CriterionObservation{a, "passed", "The second teacher saw the saved note."}
	failed := CriterionObservation{b, "failed", "The following week contained the first week's note."}
	pending := CriterionObservation{b, "pending", "The second classroom device is offline."}
	for _, tc := range []struct {
		name, revision, want string
		rows                 []CriterionObservation
	}{
		{"failed_over_pending", "build-12", "failed", []CriterionObservation{{a, "pending", "Access pending"}, failed}},
		{"partly_pending", "build-12", "pending", []CriterionObservation{passed, pending}},
		{"blocked_before_build_identified", "", "pending", []CriterionObservation{{a, "pending", "Access pending"}, pending}},
		{"passed_without_revision", "", "", []CriterionObservation{passed, pending}},
		{"failed_without_revision", "", "", []CriterionObservation{passed, failed}},
		{"missing_criterion", "build-12", "", []CriterionObservation{passed}},
		{"unknown_criterion", "build-12", "", []CriterionObservation{passed, {"unreviewed", "passed", "Observation"}}},
		{"duplicate", "build-12", "", []CriterionObservation{passed, passed}},
		{"no_observation", "build-12", "", []CriterionObservation{{a, "passed", "  "}, pending}},
		{"no_pending_reason", "build-12", "", []CriterionObservation{passed, {b, "pending", ""}}},
		{"unknown_result", "build-12", "", []CriterionObservation{passed, {b, "complete", "Observation"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := VerificationInput{Environment: "staging", Revision: tc.revision, Source: "operator", Observer: "internal tester", Observations: tc.rows}
			got, err := RecordVerification(t.Context(), issue, input, nil)
			if (err == nil) != (tc.want != "") || got.Result != tc.want {
				t.Fatal(got, err)
			}
		})
	}
}

func TestPrivateVerificationReadsIssueWithExistingGHLogin(t *testing.T) {
	selected, record := verificationFixture(t)
	t.Setenv("BUGSNITCH_GITHUB_READ_TOKEN", "")
	http.DefaultTransport = offlineGitHub(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	})
	path := t.TempDir()
	t.Setenv("PATH", path)
	data := verificationIssueJSON(t, record)
	script := "#!/bin/sh\nset -eu\ntest \"$GH_PROMPT_DISABLED\" = 1\ntest \"$*\" = 'api --hostname github.com --method GET repos/fixture/notes/issues/42'\nprintf '%s' '" + string(data) + "'\n"
	if err := os.WriteFile(filepath.Join(path, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := verificationIssue(t.Context(), selected)
	if err != nil || got.GitHubID != record.ID || got.RequirementsDigest != record.RequirementsDigest() {
		t.Fatal(got, err)
	}
}

func TestVerificationCheckRechecksIssueBeforeSaving(t *testing.T) {
	issue, record := verificationFixture(t)
	base := http.DefaultTransport
	reads := 0
	http.DefaultTransport = offlineGitHub(func(r *http.Request) (*http.Response, error) {
		reads++
		if reads == 2 {
			record.Body = "Changed during the walkthrough"
		}
		return base.RoundTrip(r)
	})
	check := filepath.Join(t.TempDir(), "observe")
	const script = `#!/bin/sh
/bin/cat >/dev/null
printf '%s' '{"environment":"staging","revision":"build-12","observations":[{"criterion":"Co-teacher can reopen the note","result":"passed","evidence":"The saved note reopened."},{"criterion":"Adjacent week is empty","result":"passed","evidence":"The adjacent week was empty."}]}'
`
	if err := os.WriteFile(check, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	input := VerificationInput{Environment: "staging", Revision: "build-12", Source: "runner-check", Observer: "fixture walkthrough"}
	receipt, err := RecordVerification(t.Context(), issue, input, []string{check})
	if err == nil || !strings.Contains(err.Error(), "issue recheck") || reads != 2 || receipt.ID != "" {
		t.Fatal("accepted a changed issue or failed before rechecking it", receipt, reads, err)
	}
	receipt, err = RecordVerification(t.Context(), issue, input, []string{check})
	if err != nil || reads != 4 || receipt.Source != "runner-check" || receipt.Result != "passed" {
		t.Fatal(receipt, reads, err)
	}
}
