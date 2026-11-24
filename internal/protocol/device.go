// SPDX-License-Identifier: MPL-2.0
package protocol

import "time"

const Version = 1

const (
	PlanReviewRequired = "plan_review_required"
	OwnerInputRequired = "owner_input_required"
	ReviewCorrection   = "review_correction"
	RecoveredCandidate = "recovered_candidate"
	OwnerPaused        = "owner_paused"
)

// WorkflowReason identifies wait states introduced with the work board.
func WorkflowReason(code string) bool {
	switch code {
	case PlanReviewRequired, OwnerInputRequired, ReviewCorrection, RecoveredCandidate, OwnerPaused:
		return true
	}
	return false
}

type Pair struct {
	Protocol   int    `json:"protocol"`
	Code       string `json:"code"`
	Credential string `json:"credential"`
	Platform   string `json:"platform"`
}
type Project struct {
	PolicyVersion int64  `json:"policyVersion,omitempty"`
	PlanID        string `json:"planId,omitempty"`
	ID            string `json:"id"`
	Repository    string `json:"repository"`
	Paused        bool   `json:"paused"`
}
type Enrollment struct {
	TurnMetering    bool      `json:"turnMetering,omitempty"`
	WorkflowReasons bool      `json:"workflowReasons,omitempty"`
	StageTimings    bool      `json:"stageTimings,omitempty"`
	Commands        []Control `json:"commands,omitempty"`
	Workspace       string    `json:"workspaceId"`
	DeviceID        string    `json:"deviceId"`
	Projects        []Project `json:"projects"`
}
type StageEvidence struct {
	DurationMS        *int64 `json:"durationMs,omitempty"`
	Phase             string `json:"phase"`
	CandidateSHA      string `json:"candidateSha,omitempty"`
	ChangedFiles      int    `json:"changedFiles"`
	ExitCode          *int   `json:"exitCode,omitempty"`
	ImportantFindings int    `json:"importantFindings"`
	MinorFindings     int    `json:"minorFindings"`
}
type Attempt struct {
	ReasonCode     string          `json:"reasonCode,omitempty"`
	IssueUpdatedAt string          `json:"issueUpdatedAt,omitempty"`
	Evidence       []StageEvidence `json:"evidence,omitempty"`
	ID             string          `json:"id"`
	ProjectID      string          `json:"projectId"`
	IssueNumber    int             `json:"issueNumber"`
	BaseSHA        string          `json:"baseSha"`
	CandidateSHA   string          `json:"candidateSha"`
	Phase          string          `json:"phase"`
	State          string          `json:"state"`
	Version        int             `json:"version"`
}
type Sync struct {
	Metering []TurnMeter `json:"metering,omitempty"`
	Acks     []Ack       `json:"acks,omitempty"`
	Protocol int         `json:"protocol"`
	Attempts []Attempt   `json:"attempts"`
}

type Admission struct {
	Scope        *RunScope `json:"scope,omitempty"`
	CriteriaHash string    `json:"criteriaHash,omitempty"`
	State        string    `json:"state,omitempty"`
	Protocol     int       `json:"protocol"`
	AttemptID    string    `json:"attemptId"`
	ProjectID    string    `json:"projectId"`
	IssueNumber  int       `json:"issueNumber"`
	BaseSHA      string    `json:"baseSha"`
	Operation    string    `json:"operation"`
	Sequence     int64     `json:"sequence"`
}

type Control struct {
	ID             string    `json:"id"`
	WorkspaceID    string    `json:"workspaceId"`
	ProjectID      string    `json:"projectId"`
	DeviceID       string    `json:"deviceId"`
	AttemptID      string    `json:"attemptId"`
	Operation      string    `json:"operation"`
	SubjectVersion int       `json:"subjectVersion"`
	ExpiresAt      time.Time `json:"expiresAt"`
}
type Ack struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type Diff struct {
	Protocol     int    `json:"protocol"`
	AttemptID    string `json:"attemptId"`
	Version      int    `json:"version"`
	BaseSHA      string `json:"baseSha"`
	CandidateSHA string `json:"candidateSha"`
	Text         string `json:"text"`
	Digest       string `json:"digest"`
}
