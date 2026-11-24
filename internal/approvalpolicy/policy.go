// SPDX-License-Identifier: MPL-2.0
// Package approvalpolicy evaluates repository policy against immutable PR data.
// Evaluate expects evidence from an authenticated observer. The local CLI also
// accepts operator-supplied evidence for policy simulations.
package approvalpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const File = ".bugsnitch/approvals.json"

type Check struct {
	Name  string `json:"name"`
	AppID int64  `json:"appId"`
}

type Requirements struct {
	ReviewDepth string   `json:"reviewDepth"`
	Owners      []string `json:"owners"`
	Checks      []Check  `json:"checks"`
}

type Rule struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
	Requirements
}

type Policy struct {
	Schema       int          `json:"schema"`
	PolicyOwners []string     `json:"policyOwners"`
	Default      Requirements `json:"default"`
	Rules        []Rule       `json:"rules"`
}

type CheckResult struct {
	Check
	Conclusion string `json:"conclusion"`
}

type Evidence struct {
	Base             string        `json:"base"`
	Head             string        `json:"head"`
	Reference        string        `json:"reference"`
	Author           string        `json:"author"`
	Reviewer         string        `json:"reviewer"`
	ReviewDepth      string        `json:"reviewDepth"`
	ReviewConclusion string        `json:"reviewConclusion"`
	Checks           []CheckResult `json:"checks"`
	Approvers        []string      `json:"approvers"`
}

type Decision struct {
	Outcome              string       `json:"outcome"`
	Base                 string       `json:"base"`
	Head                 string       `json:"head"`
	PolicyRevision       string       `json:"policyRevision"`
	PolicyDigest         string       `json:"policyDigest,omitempty"`
	ChangedPaths         []string     `json:"changedPaths,omitempty"`
	MatchedRules         []string     `json:"matchedRules,omitempty"`
	Required             Requirements `json:"required"`
	RequirementsResolved bool         `json:"requirementsResolved,omitempty"`
	EvidenceReference    string       `json:"evidenceReference,omitempty"`
	Reasons              []string     `json:"reasons,omitempty"`
}

var revision = regexp.MustCompile(`^[a-f0-9]{40}$`)
var actor = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@-]{0,99}(\[bot\])?$`)
var depths = map[string]int{"quick": 1, "standard": 2, "deep": 3}

func short(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 256 && !strings.ContainsAny(s, "\x00\r\n")
}

func validPath(s string) bool {
	return s != "" && len(s) <= 2048 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n\\") && path.Clean(s) == s && !strings.HasPrefix(s, "/") && s != "." && s != ".." && !strings.HasPrefix(s, "../")
}

func validateRequirements(r Requirements) error {
	if depths[r.ReviewDepth] == 0 || len(r.Owners) > 64 || len(r.Checks) > 64 {
		return errors.New("invalid review depth or too many requirements")
	}
	for _, name := range r.Owners {
		if !actor.MatchString(name) {
			return errors.New("invalid owner identity")
		}
	}
	for _, c := range r.Checks {
		if !short(c.Name) || c.AppID < 1 {
			return errors.New("required checks need a name and positive appId")
		}
	}
	return nil
}

// Reject duplicate keys as well as unknown fields. A shadowed policy field can
// otherwise appear to say something different from the value used by Go.
func uniqueKeys(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	switch t {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[strings.ToLower(s)] {
				return errors.New("invalid or duplicate JSON field")
			}
			seen[strings.ToLower(s)] = true
			if err := uniqueKeys(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
	case json.Delim('['):
		for d.More() {
			if err := uniqueKeys(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
	}
	return err
}

func parse(raw []byte) (Policy, error) {
	var p Policy
	if len(raw) > 64<<10 || !utf8.Valid(raw) {
		return p, errors.New("unsupported policy encoding or size")
	}
	if err := uniqueKeys(json.NewDecoder(bytes.NewReader(raw))); err != nil {
		return p, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, errors.New("invalid approval policy document")
	}
	if d.Decode(&struct{}{}) != io.EOF || p.Schema != 1 || len(p.PolicyOwners) == 0 || len(p.PolicyOwners) > 64 || len(p.Default.Checks) == 0 || len(p.Rules) > 128 {
		return p, errors.New("policy requires schema 1, policy owners and at least one default check")
	}
	if err := validateRequirements(p.Default); err != nil {
		return p, err
	}
	for _, owner := range p.PolicyOwners {
		if !actor.MatchString(owner) {
			return p, errors.New("invalid policy owner")
		}
	}
	seen := map[string]bool{}
	for _, r := range p.Rules {
		if !short(r.Name) || seen[r.Name] || len(r.Paths) == 0 || len(r.Paths) > 32 {
			return p, errors.New("rule requires a unique name and bounded path patterns")
		}
		seen[r.Name] = true
		if err := validateRequirements(r.Requirements); err != nil {
			return p, err
		}
		for _, pattern := range r.Paths {
			if !validPath(pattern) {
				return p, errors.New("invalid policy path pattern")
			}
			parts := strings.Split(pattern, "/")
			if len(parts) > 32 {
				return p, errors.New("policy pattern has too many path segments")
			}
			for _, part := range parts {
				if strings.Contains(part, "**") && part != "**" {
					return p, errors.New("** must be a complete path segment")
				}
				if _, err := path.Match(part, ""); err != nil {
					return p, errors.New("unsupported path pattern")
				}
			}
		}
	}
	return p, nil
}

// Wildcards operate within a segment. ** consumes zero or more full segments.
func matches(pattern, name string) bool {
	parts := strings.Split(name, "/")
	active := make([]bool, len(parts)+1)
	active[0] = true
	for _, pat := range strings.Split(pattern, "/") {
		next := make([]bool, len(parts)+1)
		for i, ok := range active {
			if pat == "**" {
				next[i] = ok || (i > 0 && next[i-1])
				continue
			}
			if ok && i < len(parts) {
				next[i+1], _ = path.Match(pat, parts[i])
			}
		}
		active = next
	}
	return active[len(parts)]
}

func combine(to *Requirements, from Requirements) {
	if depths[from.ReviewDepth] > depths[to.ReviewDepth] {
		to.ReviewDepth = from.ReviewDepth
	}
	for _, o := range from.Owners {
		o = strings.ToLower(o)
		if !slices.Contains(to.Owners, o) {
			to.Owners = append(to.Owners, o)
		}
	}
	for _, c := range from.Checks {
		if !slices.Contains(to.Checks, c) {
			to.Checks = append(to.Checks, c)
		}
	}
}

// Evaluate a complete literal path list (old and new names for renames) and
// trusted observations on a specific base/head. The caller controls provenance.
func Evaluate(policy []byte, policyRevision, base, head string, paths []string, e Evidence) Decision {
	out := Decision{Outcome: "hold", Base: base, Head: head, PolicyRevision: policyRevision, ChangedPaths: paths}
	if !revision.MatchString(base) || !revision.MatchString(head) || !revision.MatchString(policyRevision) || base == head || len(paths) == 0 || len(paths) > 4096 {
		out.Reasons = []string{"a changed, bounded PR with full immutable revisions is required"}
		return out
	}
	for _, p := range paths {
		if !validPath(p) {
			out.Reasons = []string{"unsupported changed path"}
			return out
		}
	}
	out.PolicyDigest = fmt.Sprintf("%x", sha256.Sum256(policy))
	p, err := parse(policy)
	if err != nil {
		out.Reasons = []string{err.Error()}
		return out
	}
	combine(&out.Required, p.Default)
	if slices.Contains(paths, File) {
		if policyRevision != base {
			out.Reasons = []string{"policy changes require the predecessor policy"}
			return out
		}
		combine(&out.Required, Requirements{Owners: p.PolicyOwners})
	}
	patterns := 0
	for _, rule := range p.Rules {
		patterns += len(rule.Paths)
	}
	if len(paths)*patterns > 100000 {
		out.Reasons = []string{"policy comparison exceeds the supported path and rule count"}
		return out
	}
	for _, r := range p.Rules {
		match := slices.ContainsFunc(r.Paths, func(pattern string) bool {
			return slices.ContainsFunc(paths, func(file string) bool { return matches(pattern, file) })
		})
		if match {
			out.MatchedRules = append(out.MatchedRules, r.Name)
			combine(&out.Required, r.Requirements)
		}
	}
	slices.Sort(out.Required.Owners)
	out.RequirementsResolved = true
	if e.Base != base || e.Head != head || !short(e.Reference) {
		out.Reasons = []string{"current evidence for the exact base and head and its reference are required"}
		return out
	}
	out.EvidenceReference = e.Reference
	if !actor.MatchString(e.Author) || !actor.MatchString(e.Reviewer) || strings.EqualFold(e.Author, e.Reviewer) || e.ReviewConclusion != "pass" || depths[e.ReviewDepth] < depths[out.Required.ReviewDepth] {
		out.Reasons = append(out.Reasons, "independent passing "+out.Required.ReviewDepth+" review required")
	}
	for _, owner := range out.Required.Owners {
		if strings.EqualFold(e.Author, owner) || !slices.ContainsFunc(e.Approvers, func(a string) bool { return strings.EqualFold(a, owner) }) {
			out.Reasons = append(out.Reasons, "approval required from "+owner)
		}
	}
	for _, required := range out.Required.Checks {
		count, passed := 0, false
		for _, result := range e.Checks {
			if result.Check == required {
				count++
				passed = result.Conclusion == "success"
			}
		}
		if count != 1 || !passed {
			out.Reasons = append(out.Reasons, fmt.Sprintf("one successful %s check from App %d required", required.Name, required.AppID))
		}
	}
	if len(out.Reasons) == 0 {
		out.Outcome = "would_allow"
	}
	return out
}
