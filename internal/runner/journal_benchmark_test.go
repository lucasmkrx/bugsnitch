// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// Measures one durable, local journal transaction through the runner's normal
// query path. Setup and the initial migration are outside the timed interval.
func BenchmarkJournalTransaction(b *testing.B) {
	b.Setenv("BUGSNITCH_HOME", b.TempDir())
	if _, err := query("CREATE TABLE bench_progress (revision INTEGER NOT NULL); INSERT INTO bench_progress VALUES (0);"); err != nil {
		b.Fatal(err)
	}
	const advance = "BEGIN IMMEDIATE; UPDATE bench_progress SET revision = revision + 1; SELECT revision FROM bench_progress; COMMIT;"
	var revision int
	for b.Loop() {
		data, err := query(advance)
		if err != nil {
			b.Fatal(err)
		}
		revision++
		if strings.TrimSpace(string(data)) != strconv.Itoa(revision) {
			b.Fatal("unexpected journal revision")
		}
	}
}

// Measure normal runner reads and optimistic stage checkpoints on a journal with
// 32 attempts, each carrying four completed stages and their observable evidence.
func BenchmarkJournalAttempts(b *testing.B) {
	for _, operation := range []string{"Get", "Save", "Summaries"} {
		b.Run(operation, func(b *testing.B) {
			b.Setenv("BUGSNITCH_HOME", b.TempDir())
			var target Attempt
			for n := 1; n <= 32; n++ {
				a := Attempt{Summary: Summary{ID: fmt.Sprintf("%032x", n), Workspace: "bench", Repository: "owner/project", IssueNumber: n, Version: 1, Phase: "review", State: "waiting", BaseSHA: strings.Repeat("a", 40), CandidateSHA: strings.Repeat("b", 40)}}
				for _, phase := range []string{"plan", "implement", "validate", "review"} {
					a.add(Artifact{Phase: phase, Summary: "Validated the requested behavior against the recorded project criteria.", ChangedFiles: []string{"src/project.go", "src/project_test.go"}})
				}
				if _, err := query(fmt.Sprintf("INSERT INTO journal_attempts VALUES(%s,'bench','owner/project',%d,1,%s,1);", literal(a.ID), n, packed(a))); err != nil {
					b.Fatal(err)
				}
				target = a
			}
			b.ReportAllocs()
			for b.Loop() {
				switch operation {
				case "Get":
					if a, err := Get(target.ID); err != nil || len(a.Artifacts) != 4 {
						b.Fatalf("get: %v", err)
					}
				case "Save":
					if err := target.save(); err != nil {
						b.Fatal(err)
					}
				case "Summaries":
					if s, err := Summaries(); err != nil || len(s) != 32 {
						b.Fatalf("summaries: %v", err)
					}
				}
			}
		})
	}
}
