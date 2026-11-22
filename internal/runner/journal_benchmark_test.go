// SPDX-License-Identifier: MPL-2.0
package runner

import (
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
