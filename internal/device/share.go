// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

func Share(args []string) error {
	fs := flag.NewFlagSet("share-diff", flag.ContinueOnError)
	id := fs.String("id", "", "local attempt")
	digest := fs.String("approve", "", "SHA-256 of the preview approved for sharing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, diff, err := runner.DiffForSharing(*id)
	if err != nil {
		return err
	}
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(diff)))
	if *digest == "" {
		fmt.Println(diff)
		fmt.Println("\nReviewed-content SHA-256:", sum)
		return nil
	}
	if *digest != sum {
		return errors.New("approved hash differs from current redacted content")
	}
	p, err := getProfile()
	if err != nil {
		return err
	}
	if p.Enrollment.Workspace != a.Workspace {
		return errors.New("workspace differs from pairing")
	}
	if err = syncOnce(context.Background()); err != nil {
		return err
	}
	var result struct{ Digest string }
	if err = send(context.Background(), p, "share-diff", protocol.Diff{Protocol: 1, AttemptID: a.ID, Version: a.Version, BaseSHA: a.BaseSHA, CandidateSHA: a.CandidateSHA, Text: diff, Digest: sum}, &result); err != nil {
		return err
	}
	if result.Digest != sum {
		return errors.New("shared digest differs from approved digest")
	}
	fmt.Println("Shared reviewed diff", sum)
	return nil
}
