// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadinessRunsValidatorOnCommittedFiles(t *testing.T) {
	dir, issue := fixture(t)
	conf := Config{Files: []string{"example.txt"}, Validate: []string{"/usr/bin/grep", "-Fxq", "broken", "example.txt"}}
	if err := AddProject("personal", issue.Repository, dir, conf); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "example.txt")
	if err := os.WriteFile(path, []byte("uncommitted\n"), 0600); err != nil {
		t.Fatal(err)
	}
	checks := CheckProjectReadiness(t.Context(), "personal", issue.Repository, dir, true)
	if len(checks) != 2 || !checks[0].Ready || !checks[1].Ready || !strings.Contains(checks[1].Message, "passed on the exported commit") {
		t.Fatal(checks)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "uncommitted\n" {
		t.Fatal(string(content), err)
	}
	conf.Validate[2] = "uncommitted"
	if err := AddProject("personal", issue.Repository, dir, conf); err != nil {
		t.Fatal(err)
	}
	checks = CheckProjectReadiness(t.Context(), "personal", issue.Repository, dir, true)
	if len(checks) != 2 || checks[1].Ready || !strings.Contains(checks[1].Message, "validation exited 1") {
		t.Fatal(checks)
	}
}
