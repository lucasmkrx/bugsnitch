// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/domain"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

func (s *Server) pairing(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct {
		Name       string
		ProjectIDs []string
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || len(b.Name) > 100 || len(b.ProjectIDs) == 0 || len(b.ProjectIDs) > 40 {
		return bad("Choose a device name and one to forty projects")
	}
	code := random(8)
	until := time.Now().UTC().Add(10 * time.Minute)
	if err := s.coordinate(r.Context(), "device.pairing", object{
		"workspace": ws, "name": b.Name, "projects": b.ProjectIDs,
		"codeHash": hash(code), "expiresAt": until,
	}, nil); err != nil {
		return err
	}
	return send(w, 201, object{"code": code, "expiresAt": until})
}

var credPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var platformPattern = regexp.MustCompile(`^(darwin|linux|windows)/(arm64|amd64)$`)
var shaPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func compatible(v int) error {
	if v != protocol.Version {
		return problem{426, "Incompatible runner protocol. Install a version supporting device protocol 1."}
	}
	return nil
}
func (s *Server) redeem(w http.ResponseWriter, r *http.Request) error {
	if !s.allow(r) {
		return problem{429, "Try again in one minute"}
	}
	var b protocol.Pair
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := compatible(b.Protocol); err != nil {
		return err
	}
	if len(b.Code) != 16 || !credPattern.MatchString(b.Credential) || !platformPattern.MatchString(b.Platform) {
		return bad("Invalid pairing payload")
	}
	var out protocol.Enrollment
	err := s.coordinate(r.Context(), "device.redeem", object{
		"codeHash": hash(b.Code), "credentialHash": hash(b.Credential),
		"platform": b.Platform, "newId": random(16),
	}, &out)
	if err != nil {
		return err
	}
	return send(w, 200, out)
}
func (s *Server) revoke(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct{}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := s.coordinate(r.Context(), "device.revoke", object{"workspace": ws, "device": r.PathValue("id")}, nil); err != nil {
		return err
	}
	return send(w, 200, object{"revoked": true})
}
func (s *Server) syncDevice(w http.ResponseWriter, r *http.Request) error {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !credPattern.MatchString(token) {
		return denied
	}
	var b protocol.Sync
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := compatible(b.Protocol); err != nil {
		return err
	}
	if len(b.Attempts) > 64 || len(b.Acks) > 64 {
		return bad("Limit each sync to 64 reports")
	}
	for _, a := range b.Attempts {
		if err := validateReport(a); err != nil {
			return err
		}
	}
	for _, ack := range b.Acks {
		if !keyPattern.MatchString(ack.ID) || !slices.Contains([]string{"received", "completed", "unavailable"}, ack.Status) {
			return bad("Invalid command acknowledgment")
		}
	}
	var out protocol.Enrollment
	err := s.coordinate(r.Context(), "device.sync", object{"credentialHash": hash(token), "sync": b}, &out)
	if err != nil {
		return err
	}
	return send(w, 200, out)
}

func validateReport(a protocol.Attempt) error {
	if a.IssueUpdatedAt != "" {
		if _, err := time.Parse(time.RFC3339, a.IssueUpdatedAt); err != nil || len(a.IssueUpdatedAt) > 40 {
			return bad("Invalid canonical issue revision")
		}
	}
	if !keyPattern.MatchString(a.ID) || a.IssueNumber < 1 || a.Version < 1 || !shaPattern.MatchString(a.BaseSHA) || (a.CandidateSHA != "" && !shaPattern.MatchString(a.CandidateSHA)) || !domain.ValidAttempt(a.Phase, a.State, a.CandidateSHA) {
		return bad("Invalid attempt metadata")
	}
	if !protocol.ValidReason(a.ReasonCode) {
		return bad("Invalid attempt reason code")
	}
	if len(a.Evidence) > 40 {
		return bad("Too many evidence records")
	}
	for _, e := range a.Evidence {
		if !slices.Contains([]string{"plan", "implement", "validate", "review", "wrap_up"}, e.Phase) || (e.CandidateSHA != "" && !shaPattern.MatchString(e.CandidateSHA)) || e.ChangedFiles < 0 || e.ImportantFindings < 0 || e.MinorFindings < 0 || (e.DurationMS != nil && (*e.DurationMS < 0 || *e.DurationMS > 86_400_000)) {
			return bad("Invalid stage evidence")
		}
	}
	return nil
}
