// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveLocalWorker uses a synthetic issue read and the real local Codex
// adapter. It performs no remote repository mutation. Explicit opt-in spends
// the signed-in user's allowance and shares only the small fixture context.
func TestLiveLocalWorker(t *testing.T) {
	if os.Getenv("BUGSNITCH_TEST_LIVE_CODEX") != "1" {
		t.Skip("set BUGSNITCH_TEST_LIVE_CODEX=1 to spend local ChatGPT allowance")
	}
	dir, issue := fixture(t)
	src := "package display\n\n// ElapsedMinutes reports the whole minutes in a nonnegative duration in seconds.\nfunc ElapsedMinutes(seconds int) int { return seconds / 100 }\n"
	testSrc := "package display\n\nimport \"testing\"\n\nfunc TestElapsedMinutes(t *testing.T) {\n for seconds,want:=range map[int]int{0:0,59:0,60:1,119:1,120:2} {\n if got:=ElapsedMinutes(seconds);got!=want{t.Errorf(\"ElapsedMinutes(%d) = %d, want %d\",seconds,got,want)}\n }\n}\n"
	for name, s := range map[string]string{"go.mod": "module fixture.local/display\n\ngo 1.24\n", "display.go": src, "display_test.go": testSrc} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := git(dir, "add", "--", "display.go", "display_test.go", "go.mod"); err != nil {
		t.Fatal(err)
	}
	if _, err := git(dir, "commit", "-m", "Add reproducible duration formatting regression"); err != nil {
		t.Fatal(err)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	// Invoking the Go tool creates a fresh build cache in the temporary HOME.
	config := Config{Files: []string{"display.go"}, Validate: []string{goBin, "test", "-count=1", "."}}
	if err = AddProject("personal", issue.Repository, dir, config); err != nil {
		t.Fatal(err)
	}
	baseline := exec.Command(goBin, "test", "-count=1", ".")
	baseline.Dir = dir
	out, err := baseline.CombinedOutput()
	var failure *exec.ExitError
	if !errors.As(err, &failure) || !(strings.Contains(string(out), "ElapsedMinutes(60) = 0, want 1") && strings.Contains(string(out), "FAIL")) {
		t.Fatalf("broken baseline not demonstrated: %s", out)
	}
	issue.Title = "Correct whole-minute rounding in elapsed duration display"
	issue.Criteria = []string{"For nonnegative seconds, ElapsedMinutes returns complete elapsed minutes, rounding down (0→0, 59→0, 60→1, 119→1, 120→2).", "Preserve the exported API and existing regression tests."}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	a, err := Start(ctx, "personal", issue.Repository, issue)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != "waiting" || a.Phase != "implement" {
		t.Fatal("expected owner plan checkpoint")
	}
	for pass := 0; pass < 3 && a.State == "waiting"; pass++ {
		a, err = Continue(ctx, a.ID, "Address the supplied regression and retain all acceptance criteria.")
		if err != nil {
			t.Fatal(err)
		}
	}
	if a.State != "completed" || a.CandidateSHA == a.BaseSHA || len(a.Artifacts) < 4 {
		t.Fatalf("local workflow ended %s/%s: %s", a.Phase, a.State, a.Message)
	}
	t.Logf("Local issue fixture, no external issue or PR. Base %s, candidate %s, attempt %s.", a.BaseSHA, a.CandidateSHA, a.ID)
	for _, stage := range a.Artifacts {
		if stage.Turn != "" {
			t.Logf("%s model=%s thread=%s turn=%s", stage.Phase, stage.Model, stage.Thread, stage.Turn)
		}
		if stage.ExitCode != nil {
			t.Logf("%s exit=%d candidate=%s", stage.Phase, *stage.ExitCode, stage.CandidateSHA)
		}
	}
}
