// SPDX-License-Identifier: MPL-2.0
package device

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShipReconcilesPublicationAndWaitsForOwnerApprovedDraft(t *testing.T) {
	var checks, pushes, releases int
	step := func(_ context.Context, op string) (ShipReceipt, error) {
		if op == "published" {
			releases++
			return ShipReceipt{}, errors.New("simulated lost response")
		}
		checks++
		state := []string{"awaiting_approval", "awaiting_publication", "queued", "reconciling", "created"}[checks-1]
		return ShipReceipt{Status: state, Number: 18}, nil
	}
	done, err := awaitDraft(t.Context(), "fixture/app", step, func(context.Context) error { pushes++; return nil }, time.Millisecond, nil)
	if err != nil || done.URL != "https://github.com/fixture/app/pull/18" || pushes != 1 || releases != 1 {
		t.Fatal(done, pushes, releases, err)
	}
	for _, tc := range []struct {
		state string
		err   error
	}{{"blocked", nil}, {"", statusError(403)}, {"", statusError(409)}} {
		_, err = awaitDraft(t.Context(), "fixture/app", func(context.Context, string) (ShipReceipt, error) { return ShipReceipt{Status: tc.state}, tc.err }, func(context.Context) error { t.Fatal("unapproved push"); return nil }, time.Millisecond, nil)
		if err == nil {
			t.Fatal("revoked or mismatched subject accepted")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, err = awaitDraft(ctx, "fixture/app", func(context.Context, string) (ShipReceipt, error) {
		return ShipReceipt{Status: "awaiting_approval"}, nil
	}, func(context.Context) error { t.Fatal("push without approval"); return nil }, time.Second, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
