// SPDX-License-Identifier: MPL-2.0
package approvalpolicy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var policy = []byte(`{"schema":1,"policyOwners":["pat"],"default":{"reviewDepth":"quick","checks":[{"name":"build","appId":17}]},"rules":[{"name":"payments","paths":["src/**/pay*.go"],"reviewDepth":"deep","owners":["Pat"],"checks":[{"name":"payments","appId":19}]}]}`)

func observed(base, head string) Evidence {
	return Evidence{Base: base, Head: head, Reference: "fixture/receipt", Author: "developer", Reviewer: "reviewer", ReviewDepth: "deep", ReviewConclusion: "pass",
		Checks: []CheckResult{{Check{"build", 17}, "success"}, {Check{"payments", 19}, "success"}}, Approvers: []string{"Pat"}}
}

func TestPolicyRequirements(t *testing.T) {
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	e := observed(base, head)
	for _, p := range []string{"src/payments.go", "src/lib/pay_v2.go"} {
		d := Evaluate(policy, base, base, head, []string{p}, e)
		if d.Outcome != "would_allow" || d.Required.ReviewDepth != "deep" || !slices.Equal(d.Required.Owners, []string{"pat"}) {
			t.Fatal(d)
		}
	}
	for _, mutate := range []func(*Evidence){
		func(e *Evidence) { e.Head = base }, func(e *Evidence) { e.Reference = "" }, func(e *Evidence) { e.Approvers = nil },
		func(e *Evidence) { e.Checks[1].AppID++ }, func(e *Evidence) { e.Checks[1].Conclusion = "pending" },
		func(e *Evidence) { e.Checks = append(e.Checks, e.Checks[0]) }, func(e *Evidence) { e.Author = "PAT" },
		func(e *Evidence) { e.Reviewer = e.Author }, func(e *Evidence) { e.ReviewDepth = "standard" },
	} {
		fresh := observed(base, head)
		mutate(&fresh)
		if d := Evaluate(policy, base, base, head, []string{"src/pay.go"}, fresh); d.Outcome != "hold" || len(d.Reasons) == 0 {
			t.Fatal(d)
		}
	}
	for _, raw := range [][]byte{[]byte(`{"schema":1,"Schema":1}`), []byte(`{"schema":2}`), []byte(strings.Replace(string(policy), `"schema":1`, `"schema":1,"forged":true`, 1))} {
		if d := Evaluate(raw, base, base, head, []string{"src/pay.go"}, e); d.Outcome != "hold" {
			t.Fatal(d)
		}
	}
}

func TestGitReplayUsesPredecessorAndBothRenamePaths(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "user.name=Test", "-c", "user.email=t@example.invalid", "-C", dir}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}
		output, err := cmd.Output()
		if err != nil {
			t.Fatal(args, string(output), err)
		}
		return strings.TrimSpace(string(output))
	}
	write := func(p, s string) {
		dest := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	write(File, string(policy))
	write("src/pay.go", "original\n")
	run("add", ".")
	run("commit", "-qm", "baseline")
	base := run("rev-parse", "HEAD")
	run("mv", "src/pay.go", "src/tools.go")
	run("commit", "-qm", "rename")
	head := run("rev-parse", "HEAD")
	relaxed := strings.ReplaceAll(string(policy), `"deep"`, `"quick"`)
	relaxed = strings.ReplaceAll(relaxed, `"pat"`, `"other"`)
	write(File, relaxed)
	run("add", File)
	run("commit", "-qm", "policy proposal")
	proposal := run("rev-parse", "HEAD")
	// A malicious working copy can change neither the source policy nor the inputs.
	write(File, `{"schema":999}`)
	in := CaseFile{Schema: 1, Cases: []HistoricalCase{
		{"clean", "calibration", "clean", "receipt/clean", base, head, observed(base, head)},
		{"change", "holdout", "regression", "incident/escaped", head, proposal, Evidence{}},
	}}
	ctx := context.Background()
	report, err := Replay(ctx, dir, proposal, in)
	if err != nil {
		t.Fatal(err)
	}
	first, second := report.Cases[0].Decision, report.Cases[1].Decision
	if first.Outcome != "would_allow" || first.PolicyRevision != proposal || !slices.Equal(first.ChangedPaths, []string{"src/pay.go", "src/tools.go"}) || !slices.Contains(first.MatchedRules, "payments") {
		t.Fatal(first)
	}
	if second.Outcome != "hold" || second.PolicyRevision != head || !slices.Equal(second.Required.Owners, []string{"pat"}) || report.ByCohort["holdout"]["regression"].Hold != 1 {
		t.Fatal(second, report)
	}
	first = Inspect(ctx, dir, base, head, observed(base, head))
	if first.Required.ReviewDepth != "deep" || first.PolicyRevision != base || first.Outcome != "would_allow" {
		t.Fatal(first)
	}
	in.Cases[1].Base, in.Cases[1].Head = base, head
	if _, err := Replay(ctx, dir, "", in); err == nil {
		t.Fatal("same change counted twice")
	}
}
