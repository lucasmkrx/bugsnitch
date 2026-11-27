// SPDX-License-Identifier: MPL-2.0
package protocol

import (
	"bytes"
	"errors"
	"image/png"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const BrowserQAScreenshotLimit = 80 << 10
const BrowserQAWireLimit = 224 << 10

var browserQAAttempt = regexp.MustCompile(`^[a-f0-9]{32}$`)
var browserQARevision = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

type BrowserQAObservation struct {
	Revision   string `json:"revision"`
	Result     string `json:"result"`
	Observed   string `json:"observed"`
	Screenshot []byte `json:"screenshot"`
}

type BrowserQAReport struct {
	Protocol    int                  `json:"protocol"`
	AttemptID   string               `json:"attemptId"`
	IssueNumber int                  `json:"issueNumber"`
	Assertion   string               `json:"assertion"`
	CheckedAt   time.Time            `json:"checkedAt"`
	Before      BrowserQAObservation `json:"before"`
	After       BrowserQAObservation `json:"after"`
}

func BrowserQAText(s string) bool {
	return len(s) <= 512 && strings.TrimSpace(s) != "" && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func (o BrowserQAObservation) Validate(revision string) error {
	if o.Revision != revision || !browserQARevision.MatchString(o.Revision) {
		return errors.New("browser check returned a different or invalid revision")
	}
	if (o.Result != "pass" && o.Result != "fail") || !BrowserQAText(o.Observed) {
		return errors.New("browser check must return pass or fail and a short observation")
	}
	if len(o.Screenshot) == 0 || len(o.Screenshot) > BrowserQAScreenshotLimit {
		return errors.New("browser check must return a PNG screenshot of at most 80 KiB")
	}
	config, err := png.DecodeConfig(bytes.NewReader(o.Screenshot))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 1600 || config.Height > 1200 {
		return errors.New("browser check screenshot must be a PNG up to 1600 by 1200 pixels")
	}
	if _, err := png.Decode(bytes.NewReader(o.Screenshot)); err != nil {
		return errors.New("browser check screenshot is an invalid PNG")
	}
	return nil
}

func (r BrowserQAReport) Validate() error {
	if r.Protocol != Version || !browserQAAttempt.MatchString(r.AttemptID) || r.IssueNumber < 1 || r.IssueNumber > 2147483647 || !BrowserQAText(r.Assertion) || r.CheckedAt.IsZero() || r.CheckedAt.After(time.Now().Add(5*time.Minute)) {
		return errors.New("browser QA report has invalid attempt, assertion or check time")
	}
	if r.Before.Revision == r.After.Revision {
		return errors.New("browser QA requires distinct before and after revisions")
	}
	if err := r.Before.Validate(r.Before.Revision); err != nil {
		return err
	}
	return r.After.Validate(r.After.Revision)
}
