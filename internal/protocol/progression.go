// SPDX-License-Identifier: MPL-2.0
package protocol

import "time"

type NextAction struct {
	AttemptID        string             `json:"attemptId"`
	Version          int                `json:"version"`
	Candidate        string             `json:"candidateSha,omitempty"`
	Kind             string             `json:"kind"`
	Reason           string             `json:"reason,omitempty"`
	Disposition      string             `json:"disposition"`
	Approver         string             `json:"approver,omitempty"`
	Answer           string             `json:"answer,omitempty"`
	Question         string             `json:"question,omitempty"`
	Feedback         string             `json:"feedback,omitempty"`
	PlanSummary      string             `json:"planSummary,omitempty"`
	DecisionKind     string             `json:"decisionKind,omitempty"`
	DecisionSubject  *CorrectionSubject `json:"decisionSubject,omitempty"`
	PreparedEvidence *PreparedSignoff   `json:"preparedEvidence,omitempty"`
	ExpiresAt        *time.Time         `json:"expiresAt,omitempty"`
	Draft            *DraftAction       `json:"draft,omitempty"`
	Pull             *ObservedPull      `json:"pull,omitempty"`
	Deployment       []ActiveRelease    `json:"deployment,omitempty"`
}

type PreparedSignoff struct {
	PullNumber         int    `json:"pullNumber"`
	InspectionVersion  int64  `json:"inspectionVersion"`
	Base               string `json:"baseSha"`
	IndependentReview  string `json:"independentReview"`
	CombinedValidation string `json:"combinedValidation"`
}

type CorrectionSubject struct {
	RootAttempt  string `json:"rootAttempt"`
	PullNumber   int    `json:"pullNumber"`
	PreviousHead string `json:"previousHead"`
	Target       string `json:"target"`
	Base         string `json:"baseSha,omitempty"`
}

type DraftAction struct {
	Target          string `json:"target"`
	Status          string `json:"status"`
	Number          int    `json:"number,omitempty"`
	ApprovalVersion int64  `json:"approvalVersion"`
}

type ObservedPull struct {
	Number    int    `json:"number"`
	Version   int64  `json:"version"`
	Head      string `json:"head"`
	Base      string `json:"base"`
	Target    string `json:"target"`
	Checks    string `json:"checks"`
	Review    string `json:"review"`
	State     string `json:"state"`
	Draft     bool   `json:"draft"`
	Watching  bool   `json:"watching"`
	Signal    int64  `json:"signal,omitempty"`
	MergedSHA string `json:"mergedSha,omitempty"`
}

type ActiveRelease struct {
	MonitorID     string `json:"monitorId"`
	IncidentID    string `json:"incidentId"`
	Environment   string `json:"environment"`
	Candidate     string `json:"candidateSha"`
	Build         string `json:"build,omitempty"`
	DeploymentKey string `json:"deploymentKey,omitempty"`
	Held          bool   `json:"held"`
	Verdict       string `json:"verdict,omitempty"`
}
