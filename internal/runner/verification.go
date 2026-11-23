// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/lucasmkrx/bugsnitch/internal/github"
)

// Operator observations and machine checks share one receipt format. A pending
// row explains what prevented the observation.
type CriterionObservation struct {
	Criterion string `json:"criterion"`
	Result    string `json:"result"`
	Evidence  string `json:"evidence"`
}

type VerificationInput struct {
	Environment  string                 `json:"environment"`
	Revision     string                 `json:"revision,omitempty"`
	Source       string                 `json:"source"`
	Observer     string                 `json:"observer"`
	Observations []CriterionObservation `json:"observations"`
}

type VerificationReceipt struct {
	ID         string `json:"id"`
	RecordedAt string `json:"recordedAt"`
	Issue      Issue  `json:"issue"`
	Result     string `json:"result"`
	VerificationInput
}

const verificationTable = "CREATE TABLE IF NOT EXISTS verification_receipts(id TEXT PRIMARY KEY,body TEXT NOT NULL); "

func validVerificationLabel(s string, limit int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= limit && strings.IndexFunc(s, unicode.IsControl) < 0
}

// Resolve either a reviewed selection or a repository/number with an
// operator-reviewed checklist. Both paths bind the receipt to the live issue.
func verificationIssue(ctx context.Context, selected Issue) (Issue, error) {
	record, err := fetchGitHubIssue(ctx, selected.Repository, selected.Number)
	var status *github.APIError
	if errors.As(err, &status) && (status.Status == 401 || status.Status == 403 || status.Status == 404) && os.Getenv("BUGSNITCH_GITHUB_READ_TOKEN") == "" {
		// gh can read private issues with the operator's existing login. Keep
		// credential material inside gh and restrict the request to GitHub.
		if gh, lookupErr := exec.LookPath("gh"); lookupErr == nil {
			read, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			path := fmt.Sprintf("repos/%s/issues/%d", selected.Repository, selected.Number)
			cmd := exec.CommandContext(read, gh, "api", "--hostname", "github.com", "--method", "GET", path)
			cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1")
			output := &capped{max: 1 << 20}
			cmd.Stdout = struct{ io.Writer }{output}
			if cmd.Run() != nil || output.overflow || json.Unmarshal(output.Bytes(), &record) != nil {
				return Issue{}, errors.New("unable to read the selected GitHub issue with gh")
			}
			err = nil
		}
	}
	if err != nil {
		return Issue{}, err
	}
	if selected.Schema == 0 {
		selected.Schema = 1
		selected.URL = fmt.Sprintf("https://github.com/%s/issues/%d", selected.Repository, selected.Number)
		selected.Title = record.Title
		selected.State = record.State
		selected.UpdatedAt = record.UpdatedAt
	}
	if err := record.matches(selected); err != nil {
		return Issue{}, err
	}
	selected.GitHubID = record.ID
	selected.RequirementsDigest = record.RequirementsDigest()
	return selected, selected.verify()
}

// Every reviewed criterion needs exactly one result. Preserve the reviewed
// order even when a test harness emits observations in another order.
func verificationResults(criteria []string, observations []CriterionObservation) ([]CriterionObservation, string, error) {
	if len(criteria) != len(observations) {
		return nil, "", errors.New("provide one observation for each reviewed criterion")
	}
	byCriterion := make(map[string]CriterionObservation, len(observations))
	for _, row := range observations {
		if strings.TrimSpace(row.Evidence) == "" || len(row.Evidence) > 4096 {
			return nil, "", errors.New("every criterion needs 1–4096 bytes of evidence or a pending reason")
		}
		if row.Result != "passed" && row.Result != "failed" && row.Result != "pending" {
			return nil, "", errors.New("criterion result must be passed, failed or pending")
		}
		if _, duplicate := byCriterion[row.Criterion]; duplicate {
			return nil, "", errors.New("multiple observations for one criterion")
		}
		byCriterion[row.Criterion] = row
	}
	result := "passed"
	ordered := make([]CriterionObservation, 0, len(criteria))
	for _, criterion := range criteria {
		row, ok := byCriterion[criterion]
		if !ok {
			return nil, "", errors.New("observation criteria must match the reviewed checklist exactly")
		}
		if row.Result == "failed" || row.Result == "pending" && result == "passed" {
			result = row.Result
		}
		ordered = append(ordered, row)
	}
	return ordered, result, nil
}

// RecordVerification writes a local evidence receipt. It does not acquire an
// implementation slot, change the repository, or submit an issue update.
func RecordVerification(ctx context.Context, selected Issue, in VerificationInput, check []string) (VerificationReceipt, error) {
	var receipt VerificationReceipt
	if !repoPattern.MatchString(selected.Repository) || selected.Number < 1 || len(selected.Criteria) == 0 || len(selected.Criteria) > 32 {
		return receipt, errors.New("select a GitHub issue and 1–32 reviewed criteria")
	}
	if selected.Schema != 0 {
		if err := selected.verify(); err != nil {
			return receipt, err
		}
	}
	seen := make(map[string]bool, len(selected.Criteria))
	for _, criterion := range selected.Criteria {
		if strings.TrimSpace(criterion) == "" || len(criterion) > 2048 || seen[criterion] {
			return receipt, errors.New("use distinct nonempty criteria of at most 2048 bytes each")
		}
		seen[criterion] = true
	}
	if !validVerificationLabel(in.Environment, 256) || !validVerificationLabel(in.Observer, 256) {
		return receipt, errors.New("provide the environment and observer, each on one line of at most 256 bytes")
	}
	if in.Revision != "" && !validVerificationLabel(in.Revision, 512) {
		return receipt, errors.New("provide an immutable build or deployment revision of at most 512 bytes")
	}
	direct := len(check) > 0
	if direct {
		if in.Source != "runner-check" || len(in.Observations) != 0 || in.Revision == "" {
			return receipt, errors.New("a runner check requires a deployment revision and collects its own observations")
		}
	} else if in.Source != "operator" && in.Source != "imported-check" {
		return receipt, errors.New("identify the source of the imported observations")
	}
	result := ""
	var err error
	if !direct {
		in.Observations, result, err = verificationResults(selected.Criteria, in.Observations)
		if err != nil {
			return receipt, err
		}
		if in.Revision == "" {
			for _, row := range in.Observations {
				if row.Result != "pending" {
					return receipt, errors.New("a passed or failed observation requires the tested revision")
				}
			}
		}
	}
	issue, err := verificationIssue(ctx, selected)
	if err != nil {
		return receipt, err
	}
	if direct {
		rows, err := runVerificationCheck(ctx, check, issue, in.Environment, in.Revision)
		if err != nil {
			return receipt, err
		}
		in.Observations, result, err = verificationResults(issue.Criteria, rows)
		if err != nil {
			return receipt, err
		}
		if _, err := verificationIssue(ctx, issue); err != nil {
			return receipt, fmt.Errorf("issue recheck after verification: %w", err)
		}
	}
	receipt = VerificationReceipt{Issue: issue, VerificationInput: in, Result: result}
	// An identical import returns the same receipt and its original recording
	// time. Each directly executed check gets its own execution record.
	if direct {
		receipt.ID, err = newID()
		if err != nil {
			return VerificationReceipt{}, err
		}
	} else {
		canonical, err := json.Marshal(receipt)
		if err != nil {
			return VerificationReceipt{}, err
		}
		digest := sha256.Sum256(canonical)
		receipt.ID = hex.EncodeToString(digest[:16])
	}
	receipt.RecordedAt = now()
	data, err := query(verificationTable + "INSERT INTO verification_receipts(id,body) VALUES(" + literal(receipt.ID) + "," + packed(receipt) + ") ON CONFLICT(id) DO NOTHING; SELECT body FROM verification_receipts WHERE id=" + literal(receipt.ID) + ";")
	if err != nil {
		return VerificationReceipt{}, err
	}
	err = json.Unmarshal(data, &receipt)
	return receipt, err
}

func GetVerification(id string) (VerificationReceipt, error) {
	var receipt VerificationReceipt
	if !idPattern.MatchString(id) {
		return receipt, errors.New("select a 32-character verification receipt ID")
	}
	data, err := query(verificationTable + "SELECT body FROM verification_receipts WHERE id=" + literal(id) + ";")
	if err != nil {
		return receipt, err
	}
	if len(data) == 0 {
		return receipt, errors.New("verification receipt missing")
	}
	err = json.Unmarshal(data, &receipt)
	return receipt, err
}
