// SPDX-License-Identifier: MPL-2.0
package incidents_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/incidents"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
	"github.com/lucasmkrx/bugsnitch/internal/testcodex"
)

const checkoutSource = "../../examples/checkout"

var siteFiles = []string{"index.html", "app.mjs", "shipping.mjs", "telemetry.mjs", "config.mjs"}

func runIn(t *testing.T, dir, bin string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", filepath.Base(bin), err, out)
	}
	return out
}

func checkoutGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	options := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}
	return runIn(t, dir, "git", append(options, args...)...)
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

type checkoutSite struct{ dir, origin string }

func newCheckoutSite(t *testing.T) checkoutSite {
	t.Helper()
	dir := t.TempDir()
	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(server.Close)
	return checkoutSite{dir, server.URL}
}

// deploy materializes the committed site and pinned SDK at a stable loopback origin.
func (site checkoutSite) deploy(t *testing.T, repo, sha, release string) string {
	t.Helper()
	sum := sha256.New()
	for _, name := range append(append([]string{}, siteFiles...), "sentry-sdk.mjs") {
		var asset []byte
		switch name {
		case "sentry-sdk.mjs":
			asset = read(t, filepath.Join(checkoutSource, name))
		case "config.mjs":
			source := string(checkoutGit(t, repo, "show", sha+":"+name))
			dsn := strings.Replace(site.origin, "://", "://public@", 1) + "/21"
			withDSN := strings.Replace(source, `export const dsn = ""`, "export const dsn = "+fmt.Sprintf("%q", dsn), 1)
			if withDSN == source {
				t.Fatal("checkout fixture requires a blank DSN")
			}
			if release == "" {
				// Keep the bundle digest outside the files it identifies.
				withDSN = strings.Replace(withDSN, `"checkout-v1"`, `(await (await fetch("/build.json")).json()).build`, 1)
			} else {
				withDSN = strings.Replace(withDSN, "checkout-v1", release, 1)
			}
			asset = []byte(withDSN)
		default:
			asset = checkoutGit(t, repo, "show", sha+":"+name)
		}
		write(t, filepath.Join(site.dir, name), asset)
		fmt.Fprintf(sum, "%s %x\n", name, sha256.Sum256(asset))
	}
	build := fmt.Sprintf("%x", sum.Sum(nil))
	if release == "" {
		write(t, filepath.Join(site.dir, "build.json"), []byte(fmt.Sprintf(`{"build":%q}`, build)))
	}
	return build
}

func browser(t *testing.T, origin, behavior, release string) map[string]any {
	t.Helper()
	out := runIn(t, "../../web", "node", "tests/checkout-probe.mjs", origin, behavior, release)
	var events []map[string]any
	if err := json.Unmarshal(out, &events); err != nil {
		t.Fatalf("invalid browser evidence: %s", out)
	}
	if behavior == "fixed" {
		if len(events) != 0 {
			t.Fatal("fixed build reported a failure")
		}
		return nil
	}
	if len(events) != 1 {
		t.Fatal("missing application error envelope")
	}
	return events[0]
}

func TestMain(m *testing.M) { testcodex.Main(m) }

// TestCheckoutAcceptance uses the production runner with scripted Codex responses.
func TestCheckoutAcceptance(t *testing.T) {
	if os.Getenv("BUGSNITCH_TEST_CHECKOUT") != "1" {
		t.Skip("run make checkout-acceptance to build the helpers and checkout SDK")
	}
	testcodex.Install(t)
	checkoutIncidentRepair(t)
}

// TestLiveCheckoutIncidentRepair spends local ChatGPT allowance on a checked-in
// disposable fixture. GitHub, Sentry's backend and its signature are fixtures.
func TestLiveCheckoutIncidentRepair(t *testing.T) {
	if os.Getenv("BUGSNITCH_TEST_LIVE_CHECKOUT") != "1" {
		t.Skip("opt in to a real local Codex repair with BUGSNITCH_TEST_LIVE_CHECKOUT=1")
	}
	checkoutIncidentRepair(t)
}

func checkoutIncidentRepair(t *testing.T) {
	if os.Getenv("BUGSNITCH_TEST_DATABASE_URL") == "" {
		t.Fatal("provide a disposable BUGSNITCH_TEST_DATABASE_URL")
	}
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("BUGSNITCH_GITHUB_READ_TOKEN", "")
	t.Setenv("BUGSNITCH_HOME", t.TempDir())
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The validator gets an empty temporary HOME. Resolve an installed Node
	// binary so user-profile version manager shims are unnecessary there.
	node := strings.TrimSpace(string(runIn(t, checkoutSource, "node", "-p", "process.execPath")))
	if !filepath.IsAbs(node) {
		t.Fatal("Node returned a nonabsolute validator executable")
	}
	for _, name := range append(append([]string{}, siteFiles...), "verify.mjs") {
		write(t, filepath.Join(repo, name), read(t, filepath.Join(checkoutSource, name)))
	}
	// The runner checks this canonical remote identity. The fixture needs only
	// local commits. Its HTTP issue requests stay within the provider double.
	checkoutGit(t, repo, "init", "-b", "main")
	checkoutGit(t, repo, "remote", "add", "origin", "https://github.com/example/disposable.git")
	checkoutGit(t, repo, "add", "--", ".")
	checkoutGit(t, repo, "commit", "-m", "Checkout baseline with a reproducible shipping threshold error")
	base := strings.TrimSpace(string(checkoutGit(t, repo, "rev-parse", "HEAD")))
	cmd := exec.Command(node, "verify.mjs")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "500 !== 0") {
		t.Fatalf("baseline did not reproduce the threshold regression: %s", out)
	}

	fixture := incidents.NewCheckoutFixture(t)
	const oldBuild = "checkout-v1"
	oldSite := newCheckoutSite(t)
	oldSite.deploy(t, repo, base, oldBuild)
	first := browser(t, oldSite.origin, "broken", oldBuild)
	fixture.Deliver(first, true, 1)
	fixture.Deliver(browser(t, oldSite.origin, "broken", oldBuild), false, 2)
	issue := runner.Issue(fixture.Promote())
	// The advertised offer and reproducer supply this owner-selected scope.
	issue.Criteria = []string{"Orders of exactly 5000 cents ship free. Preserve the $5 fee at 4999 cents and free shipping at 5001 cents.", "Keep the shippingCents API and current invalid-input behavior. The unchanged node verify.mjs must pass."}
	harness, err := filepath.Abs(filepath.Join(checkoutSource, "browser-qa.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	playwright, err := filepath.Abs("../../web/node_modules/playwright-core/index.mjs")
	if err != nil {
		t.Fatal(err)
	}
	browserCommand := []string{node, harness, playwright}
	if bin := os.Getenv("BUGSNITCH_TEST_CHROME"); bin != "" {
		if !filepath.IsAbs(bin) {
			bin, err = filepath.Abs(filepath.Join("../../web", bin))
			if err != nil {
				t.Fatal(err)
			}
		}
		browserCommand = append(browserCommand, bin)
	}
	if err = runner.AddProject("personal", issue.Repository, repo, runner.Config{
		Files: []string{"shipping.mjs"}, Validate: []string{node, "verify.mjs"},
		BrowserQA: map[int]runner.BrowserQACheck{issue.Number: {Assertion: "An order of $50 ships free.", Command: browserCommand}},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	a, err := runner.Start(ctx, "personal", issue.Repository, issue)
	if err != nil || a.State != "waiting" || a.Phase != "implement" {
		t.Fatalf("worker plan state=%s/%s err=%v", a.Phase, a.State, err)
	}
	for pass := 0; pass < 3 && a.State == "waiting"; pass++ {
		a, err = runner.Continue(ctx, a.ID, "Apply the scoped correction and retain the regression assertions.")
		if err != nil {
			t.Fatal(err)
		}
	}
	if a.State != "completed" || a.BaseSHA != base || a.CandidateSHA == base {
		t.Fatalf("worker did not complete a candidate: %s/%s", a.Phase, a.State)
	}
	var implement, review string
	var validated bool
	for _, e := range a.Artifacts {
		if e.CandidateSHA != a.CandidateSHA {
			continue
		}
		switch e.Phase {
		case "implement":
			implement = e.Thread
		case "review":
			review = e.Thread
		case "validate":
			validated = e.ExitCode != nil && *e.ExitCode == 0
		}
	}
	if !validated || review == "" || implement == "" || implement == review {
		t.Fatalf("missing validation or independent review on %s", a.CandidateSHA)
	}
	t.Logf("Local attempt %s: baseline %s failed; candidate %s passed node verify.mjs and independent review turn", a.ID, base, a.CandidateSHA)
	qa, err := runner.CaptureBrowserQA(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := qa.Validate(); err != nil || qa.Before.Revision != base || qa.After.Revision != a.CandidateSHA ||
		qa.Before.Result != "fail" || qa.After.Result != "pass" || qa.Before.Observed == qa.After.Observed {
		t.Fatalf("unexpected QA comparison: before=%s, after=%s, err=%v", qa.Before.Observed, qa.After.Observed, err)
	}
	t.Logf("Browser QA on the committed exports: %s → %s", qa.Before.Observed, qa.After.Observed)
	fixture.Observe(incidents.Receipt{Kind: "fix", Candidate: a.CandidateSHA, Evidence: "Runner validation and independent review turn, attempt " + a.ID}, "Awaiting deployment")
	monitor := fixture.Monitor(a.CandidateSHA)
	site := newCheckoutSite(t)
	build := site.deploy(t, a.Worktree, a.CandidateSHA, "")
	for deployment := 1; deployment <= 2; deployment++ {
		if deployment == 2 && site.deploy(t, a.Worktree, a.CandidateSHA, "") != build {
			t.Fatal("redeployed candidate has a different asset manifest")
		}
		fixture.Deployed(monitor, deployment, build)
		started := time.Now().UTC()
		browser(t, site.origin, "fixed", build)
		fixture.Verified(monitor, deployment, build, started)
	}
	oldReport := browser(t, oldSite.origin, "broken", oldBuild)
	fixture.Deliver(oldReport, false, 3)
	fixture.Observe(incidents.Receipt{}, "Verified in environment")
	// Inject a contradictory report against the fixed build to exercise
	// invalidation. The preceding live browser probe succeeded on this build.
	oldReport["event_id"] = "a381d617b02448d3943e853f12c4f377"
	oldReport["release"] = build
	fixture.Deliver(oldReport, false, 4)
	fixture.Observe(incidents.Receipt{}, "Needs triage")
	fixture.CheckGate(monitor, 2, build, "current-build recurrence", false)
	t.Logf("Signed deployment evidence and browser-validated bundle %s, followed by fixture current-build recurrence", build)
}
