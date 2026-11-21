// SPDX-License-Identifier: MPL-2.0
package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestRemoteFailureClasses(t *testing.T) {
	for in, want := range map[string]Failure{`"usageLimitExceeded"`: Allowance, `"rateLimitExceeded"`: RateLimit, `"unauthorized"`: SignIn, `"modelNotFound"`: UnavailableModel, `{"responseStreamDisconnected":{"httpStatusCode":503}}`: Provider} {
		if got := classify(json.RawMessage(in)); !errors.Is(got, want) {
			t.Errorf("%s: %s", in, got)
		}
	}
}

type input struct{ bytes.Buffer }

func (*input) Close() error { return nil }
func TestRejectRuntimeServerRequests(t *testing.T) {
	w := &input{}
	c := Client{in: w, out: bufio.NewScanner(strings.NewReader("{\"id\":123,\"method\":\"item/commandExecution/requestApproval\"}\n{\"id\":1,\"result\":{}}\n"))}
	if err := c.call("initialize", map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	var messages []map[string]any
	d := json.NewDecoder(&w.Buffer)
	for {
		var m map[string]any
		if err := d.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, m)
	}
	if len(messages) != 2 || messages[1]["error"] == nil || messages[1]["result"] != nil {
		t.Fatal("server request was not rejected")
	}
	t.Setenv("OPENAI_API_KEY", "synthetic")
	t.Setenv("BUGSNITCH_GITHUB_READ_TOKEN", "synthetic")
	if slices.ContainsFunc(localEnv(), func(s string) bool { return strings.Contains(s, "synthetic") }) {
		t.Fatal("unrelated credentials passed to app-server")
	}
}
