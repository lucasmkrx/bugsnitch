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
func post(handler http.Handler, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
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
	payload := errorPayload(eventOne, "v2", "production", time.Now())
	o, err := normalizeError(payload)
	if err != nil {
		t.Fatal(err)
	}
	bytes, _ := json.Marshal(o)
	if strings.Contains(string(bytes), markerSecret) || len(o.Frames) != 1 || o.Frames[0].File != "main.js" || o.Observed != "TypeError reported by Sentry" {
		t.Fatalf("unexpected projected fields: %s", bytes)
	}
	msg := "Cart total differed\nAuthorization: Bearer " + markerSecret + "\nEmail mail@example.invalid\nhttps://x.test/path?debug=" + markerSecret
	x := Redact(msg)
	if !strings.Contains(x, "Cart total") || strings.Contains(x, markerSecret) || strings.Contains(x, "@") {
		t.Fatalf("redaction failed")
	}
}
func TestGitHubPublishedSignatureVector(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader("Hello, World!"))
	req.Header.Set("X-Hub-Signature-256", "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17")
	if _, ok := signedBody(httptest.NewRecorder(), req, "It's a Secret to Everybody", "X-Hub-Signature-256", "sha256="); !ok {
		t.Fatal("official signature vector rejected")
	}
}
func TestSignatureIsOverOriginalBytes(t *testing.T) {
	good := errorPayload(eventOne, "v2", "production", time.Now())
	changed := append([]byte(" "), good...)
	w := post(Store{}.SentryWebhook("test-secret"), changed, map[string]string{"Sentry-Hook-Signature": sign("test-secret", good), "Sentry-Hook-Resource": "error"})
	if w.Code != 401 {
		t.Fatalf("HTTP %d", w.Code)
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

func TestSentryLabelProjectionFromTags(t *testing.T) {
	before := errorPayload(eventOne, "", "", time.Now())
	var p map[string]any
	if err := json.Unmarshal(before, &p); err != nil {
		t.Fatal(err)
	}
	e := p["data"].(map[string]any)["error"].(map[string]any)
	e["tags"] = []any{[]any{"environment", "staging"}, []any{"release", "v1"}, []any{"customer", markerSecret}}
	payload, _ := json.Marshal(p)
	got, err := normalizeError(payload)
	if err != nil || got.Environment != "staging" || got.Release != "v1" {
		t.Fatal(got, err)
	}
}
