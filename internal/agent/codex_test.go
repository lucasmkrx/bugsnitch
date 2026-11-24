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

func TestUsagePreservesUnknownAndDropsAccountIdentity(t *testing.T) {
	frame := `{"id":1,"result":{"accountId":"PRIVATE-ACCOUNT","ordinaryUsageAllowed":null,"rateLimits":{"primary":{"usedPercent":31,"resetsAt":null,"windowDurationMins":300},"credits":{"balance":"SENSITIVE"}}}}`
	c := Client{in: &input{}, out: bufio.NewScanner(strings.NewReader(frame + "\n"))}
	u, err := c.Usage()
	if err != nil || u.OrdinaryUsageAllowed != nil || u.RateLimits.Secondary != nil || u.RateLimits.Primary.ResetsAt != nil || *u.RateLimits.Primary.UsedPercent != 31 {
		t.Fatal("unknown usage detail fabricated", err)
	}
	out, _ := json.Marshal(u)
	if strings.Contains(string(out), "PRIVATE") || strings.Contains(string(out), "SENSITIVE") {
		t.Fatal("unexpected account metadata exposed")
	}
}

func TestCodexTurnMeteringBindsSingleThreadAndTurn(t *testing.T) {
	messages := `{"id":1,"result":{"data":[{"id":"fixture-model","model":"fixture-model","isDefault":true}]}}
{"id":2,"result":{"thread":{"id":"thread1"},"model":"fixture-model","modelProvider":"openai","cwd":"/work","approvalPolicy":"never","sandbox":{"type":"readOnly","networkAccess":false}}}
{"id":3,"result":{"data":[]}}
{"id":4,"result":{"turn":{"id":"turn1"}}}
{"method":"thread/tokenUsage/updated","params":{"threadId":"thread1","turnId":"turn1","tokenUsage":{"total":{"inputTokens":33,"outputTokens":10},"last":{"inputTokens":2,"outputTokens":4}}}}
{"method":"thread/tokenUsage/updated","params":{"threadId":"thread2","turnId":"turn1","tokenUsage":{"total":{"inputTokens":999,"outputTokens":999}}}}
{"method":"thread/tokenUsage/updated","params":{"threadId":"thread1","turnId":"turn2","tokenUsage":{"total":{"inputTokens":888,"outputTokens":888}}}}
{"method":"item/completed","params":{"threadId":"thread1","turnId":"turn1","item":{"type":"agentMessage","text":"done","phase":"final_answer"}}}
{"method":"turn/completed","params":{"threadId":"thread1","turn":{"id":"turn1","status":"completed"}}}
`
	c := Client{in: &input{}, dir: "/work", out: bufio.NewScanner(strings.NewReader(messages))}
	r, err := c.Turn("work", "example", "", "", nil)
	if err != nil || r.Usage == nil || r.Usage.InputTokens != 33 || r.Usage.OutputTokens != 10 {
		t.Fatalf("usage %+v, err %v", r.Usage, err)
	}
}
