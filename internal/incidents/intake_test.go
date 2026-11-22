package incidents

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const installation = "acc7e358-7800-440a-afeb-5be7864b6b70"
const eventOne = "123456789abcdef0123456789abcdef0"
const markerSecret = "PRIVATE_EVIDENCE_MARKER"

func errorPayload(id, release, environment string, at time.Time) []byte {
	body, _ := json.Marshal(map[string]any{"action": "created", "installation": map[string]string{"uuid": installation}, "data": map[string]any{"error": map[string]any{
		"project": 21, "issue_id": "42", "event_id": id, "release": release, "environment": environment, "datetime": at,
		"request": map[string]string{"cookies": markerSecret}, "user": map[string]string{"email": markerSecret},
		"exception": map[string]any{"values": []any{map[string]any{"type": "TypeError", "value": markerSecret, "stacktrace": map[string]any{"frames": []any{map[string]any{"filename": "/user/private/main.js?token=" + markerSecret, "lineno": 19, "in_app": true, "vars": markerSecret, "context_line": markerSecret}}}}}},
	}}})
	return body
}
func post(handler http.Handler, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, bytes.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}
func sign(key string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
func TestOnlyApprovedErrorFieldsSurvive(t *testing.T) {
	msg := "Cart total differed\nAuthorization: Bearer " + markerSecret + "\nEmail mail@example.invalid\nhttps://x.test/path?debug=" + markerSecret
	x := Redact(msg)
	if !strings.Contains(x, "Cart total") || strings.Contains(x, markerSecret) || strings.Contains(x, "@") {
		t.Fatalf("redaction failed")
	}
}
func TestVerificationGates(t *testing.T) {
	now := time.Now()
	yes := true
	count := int64(20)
	fix := &Receipt{Candidate: "a"}
	deploy := &Receipt{Candidate: "a", At: now.Add(-2 * time.Hour), WindowSeconds: 3600, MinimumExposures: 5}
	v := Verdict{Fix: fix, Deployment: deploy}
	if got := evaluate(v, now, false, false).Status; got != "Verification inconclusive" {
		t.Fatal(got)
	}
	v.Validation = &Receipt{Positive: &yes, At: now}
	if got := evaluate(v, now, false, false).Status; got != "Coverage unknown" {
		t.Fatal(got)
	}
	v.Validation.Coverage = &yes
	v.Validation.Exposures = &count
	v.Validation.At = deploy.At
	if got := evaluate(v, now, false, false).Status; got != "Monitoring" {
		t.Fatal(got)
	}
	v.Validation.At = now
	if got := evaluate(v, now, false, false).Status; got != "Verified in environment" {
		t.Fatal(got)
	}
	if got := evaluate(v, now, true, false).Status; got != "Needs triage" {
		t.Fatal(got)
	}
	if got := evaluate(v, now, false, true).Status; got != "Verification inconclusive" {
		t.Fatal(got)
	}
	deploy.Candidate = "old"
	if got := evaluate(v, now, false, false).Status; got != "Awaiting deployment" {
		t.Fatal(got)
	}
}
