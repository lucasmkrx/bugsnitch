// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
	"github.com/lucasmkrx/bugsnitch/internal/journalbridge"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

type Summary struct {
	Scope          *protocol.RunScope `json:"-"`
	ReasonCode     string             `json:"reasonCode,omitempty"`
	IssueUpdatedAt string             `json:"issueUpdatedAt,omitempty"`
	LocalPath      string             `json:"-"`
	CriteriaHash   string             `json:"-"`
	Evidence       []StageEvidence    `json:"evidence,omitempty"`
	ID             string             `json:"id"`
	Workspace      string             `json:"workspace"`
	Repository     string             `json:"repository"`
	IssueNumber    int                `json:"issueNumber"`
	BaseSHA        string             `json:"baseSha"`
	CandidateSHA   string             `json:"candidateSha"`
	Phase          string             `json:"phase"`
	State          string             `json:"state"`
	Version        int                `json:"version"`
}
type StageEvidence struct {
	DurationMS        *int64 `json:"durationMs,omitempty"`
	Phase             string `json:"phase"`
	CandidateSHA      string `json:"candidateSha,omitempty"`
	ChangedFiles      int    `json:"changedFiles,omitempty"`
	ExitCode          *int   `json:"exitCode,omitempty"`
	ImportantFindings int    `json:"importantFindings,omitempty"`
	MinorFindings     int    `json:"minorFindings,omitempty"`
}
type StageSettings struct {
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	Reasoning      string `json:"reasoning,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}
type Config struct {
	ValidationSandbox        string                   `json:"validationSandbox,omitempty"`
	Stages                   map[string]StageSettings `json:"stages,omitempty"`
	ValidationTimeoutSeconds int                      `json:"validationTimeoutSeconds,omitempty"`
	Files                    []string                 `json:"files"`
	Validate                 []string                 `json:"validate"`
	Model                    string                   `json:"model,omitempty"`
	Reasoning                string                   `json:"reasoning,omitempty"`
}
type Project struct {
	Workspace, Repository, Path string
	Config                      Config
}
type Issue protocol.IssueSelection

type Finding struct {
	Severity  string `json:"severity"`
	Location  string `json:"location"`
	Rationale string `json:"rationale"`
}
type Artifact struct {
	ValidationSandbox                                                                    string            `json:"validationSandbox,omitempty"`
	Usage                                                                                *agent.ModelUsage `json:"usage,omitempty"`
	DurationMS                                                                           *int64            `json:"durationMs,omitempty"`
	ID                                                                                   string            `json:"id,omitempty"`
	Phase, At, BaseSHA, CandidateSHA, IssueVersion, Policy, Thread, Turn, Model, Summary string
	ChangedFiles, Command                                                                []string  `json:",omitempty"`
	ExitCode                                                                             *int      `json:",omitempty"`
	Findings                                                                             []Finding `json:",omitempty"`
}
type CandidateIntent struct {
	Parent   string
	SHA256   map[string]string
	Artifact Artifact
}
type Attempt struct {
	LocalRunScope   *protocol.RunScope `json:"localRunScope,omitempty"`
	OwnerAnswer     string             `json:"ownerAnswer,omitempty"`
	OwnerQuestion   string             `json:"ownerQuestion,omitempty"`
	PreviousAttempt string             `json:",omitempty"`
	RetryReason     string             `json:",omitempty"`
	Intent          *CandidateIntent   `json:",omitempty"`
	Summary
	Project                                                           Project
	Issue                                                             Issue
	Branch, Worktree, CreatedAt, UpdatedAt, Message, Feedback, PRBody string
	Artifacts                                                         []Artifact
	Rounds                                                            int
}

var BeforeStart, AfterFinish, AfterTurn func(Summary) error
var BeforeTurn func(context.Context, Summary) error
var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var repoPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*/[a-zA-Z0-9_.-]+$`)
var workspacePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,99}$`)

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func newID() (string, error) {
	var nonce [16]byte
	_, err := rand.Read(nonce[:])
	return hex.EncodeToString(nonce[:]), err
}
func home() (string, error) {
	dir := os.Getenv("BUGSNITCH_HOME")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "bugsnitch")
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("BUGSNITCH_HOME must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(dir)
}
func literal(s string) string { return "CAST(X'" + hex.EncodeToString([]byte(s)) + "' AS TEXT)" }
func packed(v any) string     { b, _ := json.Marshal(v); return literal(string(b)) }
func query(q string) ([]byte, error) {
	dir, err := home()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return journalbridge.Query(ctx, filepath.Join(dir, "runner.sqlite"), q)
}

func project(w, r string) (Project, error) {
	var p Project
	b, err := query("SELECT data FROM local_projects WHERE workspace=" + literal(w) + " AND repo=" + literal(strings.ToLower(r)) + ";")
	if err != nil {
		return p, err
	}
	if len(b) == 0 {
		return p, errors.New("local project missing; use project-add")
	}
	err = json.Unmarshal(b, &p)
	return p, err
}

type checkpoint struct {
	ID      string   `json:"id"`
	Version int      `json:"version"`
	Data    *Attempt `json:"data"`
	Claim   bool     `json:"claim_with_limit"`
}

func journalAttempt(action string, args any) ([]byte, error) {
	dir, err := home()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return journalbridge.Operation(ctx, filepath.Join(dir, "runner.sqlite"), action, args)
}

func Get(id string) (Attempt, error) {
	var a Attempt
	if !idPattern.MatchString(id) {
		return a, errors.New("invalid attempt id")
	}
	b, err := journalAttempt("get", struct {
		ID string `json:"id"`
	}{id})
	if err != nil {
		return a, err
	}
	if len(b) == 0 {
		return a, errors.New("attempt missing")
	}
	err = json.Unmarshal(b, &a)
	return a, err
}
func Summaries() ([]Summary, error) {
	b, err := journalAttempt("summaries", struct{}{})
	if err != nil {
		return nil, err
	}
	var records []Summary
	err = json.Unmarshal(b, &records)
	return records, err
}
func (a *Attempt) save() error { return a.persist(false) }
func (a *Attempt) claimRunning() error {
	// A lost process still recorded as running holds capacity until reconciled.
	return a.persist(true)
}
func (a *Attempt) persist(claim bool) error {
	next := *a
	next.Version++
	next.UpdatedAt = now()
	b, err := journalAttempt("save", checkpoint{ID: a.ID, Version: a.Version, Data: &next, Claim: claim})
	if err != nil {
		return err
	}
	if string(b) != "1" {
		if claim {
			return errors.New("local ticket capacity reserved or attempt revision changed")
		}
		return errors.New("attempt changed in another process")
	}
	a.Version, a.UpdatedAt = next.Version, next.UpdatedAt
	return nil
}
func (a *Attempt) add(artifact Artifact) {
	artifact.At = now()
	artifact.BaseSHA = a.BaseSHA
	artifact.CandidateSHA = a.CandidateSHA
	artifact.IssueVersion = a.Issue.UpdatedAt
	artifact.Policy = "local-reviewed-files/v1"
	identity, _ := json.Marshal(artifact)
	artifact.ID = fmt.Sprintf("%x", sha256.Sum256(identity))
	a.Artifacts = append(a.Artifacts, artifact)
	evidence := StageEvidence{DurationMS: artifact.DurationMS, Phase: artifact.Phase, CandidateSHA: artifact.CandidateSHA, ChangedFiles: len(artifact.ChangedFiles), ExitCode: artifact.ExitCode}
	for _, f := range artifact.Findings {
		if f.Severity == "important" {
			evidence.ImportantFindings++
		} else {
			evidence.MinorFindings++
		}
	}
	a.Evidence = append(a.Evidence, evidence)
}
