// SPDX-License-Identifier: MPL-2.0
// Package testcodex provides the scripted app-server used by integration tests.
package testcodex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
)

type object = map[string]any

// Main serves the scripted app-server protocol when the test executable is
// launched through Install's private symlink.
func Main(m *testing.M) {
	if filepath.Base(os.Args[0]) == "codex" {
		os.Exit(codexProtocolFixture(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func codexProtocolFixture(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("codex-cli " + agent.Version)
		return 0
	}
	if len(args) == 0 || args[0] != "app-server" {
		return 1
	}
	in := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	tid := fmt.Sprintf("fixture-thread-%d", os.Getpid())
	turn := "fixture-turn"
	answer := "Plan to change the approved text file."
	for in.Scan() {
		var req struct {
			ID     *int
			Method string
			Params struct {
				Cwd, DeveloperInstructions string
				Input                      []struct{ Text string }
			}
		}
		if json.Unmarshal(in.Bytes(), &req) != nil {
			return 1
		}
		if req.ID == nil {
			continue
		}
		var out any = object{}
		switch req.Method {
		case "initialize":
		case "account/read":
			out = object{"account": object{"type": "chatgpt"}}
		case "mcpServerStatus/list":
			out = object{"data": []any{}}
		case "model/list":
			out = object{"data": []any{object{"id": "fixture", "model": "fixture", "isDefault": true}}}
		case "thread/start":
			if strings.Contains(req.Params.DeveloperInstructions, "Return disposition ready") {
				answer = `{"summary":"Change the approved file and run the specified validation","disposition":"ready","question":""}`
			}
			if strings.Contains(req.Params.DeveloperInstructions, "overall project planner") {
				answer = `{"title":"Fixture draft","milestone":"Ready","assumptions":"Test-only protocol data","target":"","forecast":"","tickets":[{"number":1,"title":"Check label","criteria":["Label visible"],"verification":"Run fixture","estimateHours":1,"priority":1,"dependencies":[],"resources":[]}]}`
			}
			if strings.Contains(req.Params.DeveloperInstructions, "project manager responding") {
				answer = "Fixture manager reply."
			}
			if strings.Contains(req.Params.DeveloperInstructions, "complete UTF-8 replacement") {
				answer = ""
			}
			if strings.Contains(req.Params.DeveloperInstructions, "Independently inspect") {
				answer = `{"summary":"Fixture review checked the candidate","findings":[]}`
			}
			out = object{"thread": object{"id": tid}, "cwd": req.Params.Cwd, "sandbox": object{"type": "readOnly"}, "model": "fixture", "modelProvider": "openai", "approvalPolicy": "never"}
		case "turn/start":
			if answer == "" {
				var task struct {
					Issue struct {
						Repository string
						Number     int
					}
					Files map[string]string
				}
				if len(req.Params.Input) != 1 || json.Unmarshal([]byte(req.Params.Input[0].Text), &task) != nil {
					return 1
				}
				path := "label.txt"
				content := "fixed\n"
				switch task.Issue.Repository {
				case "example/disposable":
					path = "shipping.mjs"
					source := task.Files[path]
					if strings.Count(source, "subtotalCents > 5000") != 1 {
						return 1
					}
					content = strings.Replace(source, "subtotalCents > 5000", "subtotalCents >= 5000", 1)
				case "fixture/batch":
					path = fmt.Sprintf("label-%d.txt", task.Issue.Number)
				}
				if _, approved := task.Files[path]; !approved {
					return 1
				}
				edit, err := json.Marshal(object{"summary": "Updated example", "edits": []object{{"path": path, "content": content}}})
				if err != nil {
					return 1
				}
				answer = string(edit)
			}
			out = object{"turn": object{"id": turn}}
		default:
			return 1
		}
		if encoder.Encode(object{"id": *req.ID, "result": out}) != nil {
			return 1
		}
		if req.Method == "turn/start" {
			encoder.Encode(object{"method": "thread/tokenUsage/updated", "params": object{"threadId": tid, "turnId": turn, "tokenUsage": object{"total": object{"inputTokens": 41, "outputTokens": 6}}}})
			encoder.Encode(object{"method": "item/completed", "params": object{"threadId": tid, "turnId": turn, "item": object{"type": "agentMessage", "text": answer, "phase": "final_answer"}}})
			encoder.Encode(object{"method": "turn/completed", "params": object{"threadId": tid, "turn": object{"id": turn, "status": "completed"}}})
		}
	}
	return 0
}

// Install puts this test executable first on PATH for the runner's Codex calls.
func Install(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err = os.Symlink(executable, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
