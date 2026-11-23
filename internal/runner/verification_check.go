// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"unicode/utf8"
)

// The operator supplies a trusted foreground executable and its literal
// arguments. A private working directory keeps relative writes out of the
// checkout. Only evidence on stdout is eligible for a receipt.
func runVerificationCheck(ctx context.Context, argv []string, issue Issue, environment, revision string) ([]CriterionObservation, error) {
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return nil, errors.New("verification check requires an absolute executable path")
	}
	input, err := json.Marshal(struct {
		Issue       Issue  `json:"issue"`
		Environment string `json:"environment"`
		Revision    string `json:"revision"`
	}{issue, environment, revision})
	if err != nil {
		return nil, errors.New("unable to encode verification check input")
	}
	dir, err := os.MkdirTemp("", "bugsnitch-verification-")
	if err != nil {
		return nil, errors.New("unable to create private verification check directory")
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + dir, "TMPDIR=" + dir, "PATH=/usr/bin:/bin", "LC_ALL=C"}
	cmd.Stdin = bytes.NewReader(append(input, '\n'))
	output := capped{max: 256 << 10}
	// Only expose Write so io.Copy cannot bypass the cap via bytes.Buffer.ReadFrom.
	cmd.Stdout = struct{ io.Writer }{&output}
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("verification check interrupted: %w", ctx.Err())
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("verification check exited with status %d", exit.ExitCode())
		}
		return nil, errors.New("verification check failed to start or finish")
	}
	if output.overflow {
		return nil, errors.New("verification check output exceeds 256 KiB")
	}
	var result struct {
		Environment  string                 `json:"environment"`
		Revision     string                 `json:"revision"`
		Observations []CriterionObservation `json:"observations"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	if !utf8.Valid(output.Bytes()) || decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF || result.Observations == nil {
		return nil, errors.New("verification check must return one JSON object containing its environment, revision and observations")
	}
	if result.Environment != environment || result.Revision != revision {
		return nil, errors.New("verification check reported a different environment or revision")
	}
	return result.Observations, nil
}
