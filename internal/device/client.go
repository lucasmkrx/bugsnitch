// SPDX-License-Identifier: MPL-2.0
// Package device publishes bounded local execution metadata to the owner's service.
package device

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/journalbridge"
	"github.com/lucasmkrx/bugsnitch/internal/protocol"
	"github.com/lucasmkrx/bugsnitch/internal/runner"
)

type statusError int

func (s statusError) Error() string { return fmt.Sprintf("device service returned HTTP %d", s) }

type profile struct {
	NextCredential           string `json:",omitempty"`
	Server, Credential, Code string
	Enrollment               protocol.Enrollment
}

func folder() (string, error) {
	dir := os.Getenv("BUGSNITCH_HOME")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "bugsnitch")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}
func sqlValue(v string) string { return "CAST(X'" + hex.EncodeToString([]byte(v)) + "' AS TEXT)" }

func journal(query string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	return journalContext(ctx, query)
}
func journalContext(ctx context.Context, query string) (string, error) {
	dir, err := folder()
	if err != nil {
		return "", err
	}
	file := filepath.Join(dir, "device.sqlite")
	b, err := journalbridge.Query(ctx, file, query)
	if err != nil {
		return "", fmt.Errorf("device journal: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}
func getProfile() (profile, error) {
	var p profile
	s, err := journal("SELECT data FROM profile WHERE id=1;")
	if err != nil {
		return p, err
	}
	if s == "" {
		return p, errors.New("device is unpaired")
	}
	err = json.Unmarshal([]byte(s), &p)
	return p, err
}
func save(p profile, new bool) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	stmt := "UPDATE profile SET data=" + sqlValue(string(b)) + " WHERE id=1 AND json_extract(data,'$.Credential')=" + sqlValue(p.Credential) + ";"
	if new {
		stmt = "INSERT INTO profile(id,data) VALUES(1," + sqlValue(string(b)) + ");"
	}
	_, err = journal(stmt)
	return err
}
func send(ctx context.Context, p profile, route string, in any, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", p.Server+"/api/v1/device/"+route, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if route != "pair" {
		req.Header.Set("Authorization", "Bearer "+p.Credential)
	}
	client := http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("device service unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return statusError(resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(out)
}
func validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path == "" && (u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()))
}
func pair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	origin := fs.String("server", "", "Bugsnitch service origin")
	code := fs.String("code", "", "one-time code")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := getProfile()
	if err != nil {
		if *origin == "" || !validURL(*origin) || len(*code) != 16 {
			return errors.New("use pair --server <https origin> --code <pairing code>")
		}
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return err
		}
		p = profile{Server: *origin, Credential: hex.EncodeToString(key), Code: *code}
		if err = save(p, true); err != nil {
			return err
		}
	} else if p.Enrollment.DeviceID != "" {
		return errors.New("device already paired")
	} else if (*origin != "" && *origin != p.Server) || (*code != "" && *code != p.Code) {
		return errors.New("another pairing is pending; unpair before replacing it")
	}
	err = send(context.Background(), p, "pair", protocol.Pair{Protocol: 1, Code: p.Code, Credential: p.Credential, Platform: runtime.GOOS + "/" + runtime.GOARCH}, &p.Enrollment)
	if err != nil {
		return err
	}
	p.Code = ""
	if err = save(p, false); err != nil {
		return err
	}
	fmt.Println("Paired computer", p.Enrollment.DeviceID)
	for _, proj := range p.Enrollment.Projects {
		fmt.Println(proj.ID, proj.Repository)
	}
	return nil
}
func register(args []string) error {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	project := fs.String("project", "", "assigned project id")
	path := fs.String("path", "", "local Git root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := getProfile()
	if err != nil {
		return err
	}
	var repository string
	for _, v := range p.Enrollment.Projects {
		if v.ID == *project {
			repository = v.Repository
		}
	}
	if repository == "" || *path == "" {
		return errors.New("choose an assigned project and its checkout")
	}
	root, err := filepath.Abs(*path)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	top, err := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel").Output()
	if err != nil || strings.TrimSpace(string(top)) != root {
		return errors.New("select the canonical Git checkout root")
	}
	remote, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").Output()
	if err != nil {
		return errors.New("checkout has no origin")
	}
	raw := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(string(remote)), ".git"))
	if raw != "git@github.com:"+repository && raw != "https://github.com/"+repository && raw != "ssh://git@github.com/"+repository {
		return errors.New("checkout origin does not match assigned repository")
	}
	_, err = journal("INSERT INTO mappings VALUES(" + sqlValue(*project) + "," + sqlValue(root) + ") ON CONFLICT(id) DO UPDATE SET path=excluded.path;")
	return err
}
func syncOnce(ctx context.Context) error {
	p, err := getProfile()
	if err != nil {
		return err
	}
	if p.Enrollment.DeviceID == "" {
		return errors.New("complete pairing first")
	}
	var assigned protocol.Enrollment
	if err = send(ctx, p, "sync", protocol.Sync{Protocol: 1}, &assigned); err != nil {
		return err
	}
	completionErr := flushEndings(p.Enrollment.DeviceID)
	var reports []protocol.Attempt
	available, err := runner.Summaries()
	if err != nil {
		return err
	}
	for _, pr := range assigned.Projects {
		mapped, err := journal("SELECT 1 FROM mappings WHERE id=" + sqlValue(pr.ID) + ";")
		if err != nil {
			return err
		}
		if mapped != "1" {
			continue
		}
		for _, a := range available {
			if a.Workspace == assigned.Workspace && strings.EqualFold(a.Repository, pr.Repository) {
				owns, err := journal("SELECT 1 FROM authority WHERE attempt=" + sqlValue(a.ID) + " AND device=" + sqlValue(p.Enrollment.DeviceID) + " AND acked>0;")
				if err != nil {
					return err
				}
				if owns != "1" {
					continue
				}
				var evidence []protocol.StageEvidence
				for _, e := range a.Evidence {
					var duration *int64
					if assigned.StageTimings {
						duration = e.DurationMS
					}
					evidence = append(evidence, protocol.StageEvidence{DurationMS: duration, Phase: e.Phase, CandidateSHA: e.CandidateSHA, ChangedFiles: e.ChangedFiles, ExitCode: e.ExitCode, ImportantFindings: e.ImportantFindings, MinorFindings: e.MinorFindings})
				}
				reports = append(reports, protocol.Attempt{ReasonCode: a.ReasonCode, IssueUpdatedAt: a.IssueUpdatedAt, Evidence: evidence, ID: a.ID, ProjectID: pr.ID, IssueNumber: a.IssueNumber, BaseSHA: a.BaseSHA, CandidateSHA: a.CandidateSHA, Phase: a.Phase, State: a.State, Version: a.Version})
			}
		}
	}
	for _, c := range assigned.Commands {
		if c.DeviceID != p.Enrollment.DeviceID || c.WorkspaceID != assigned.Workspace || time.Now().After(c.ExpiresAt) {
			continue
		}
		ack := "unavailable"
		for _, a := range reports {
			if a.ID == c.AttemptID && a.ProjectID == c.ProjectID {
				ack, err = runner.ApplyControl(c.ID, c.AttemptID, c.Operation, c.SubjectVersion)
				if err != nil {
					return err
				}
				break
			}
		}
		var updated protocol.Enrollment
		if err = send(ctx, p, "sync", protocol.Sync{Protocol: 1, Acks: []protocol.Ack{{ID: c.ID, Status: ack}}}, &updated); err != nil {
			return err
		}
	}
	for len(reports) > 0 {
		n := min(8, len(reports))
		if err = send(ctx, p, "sync", protocol.Sync{Protocol: 1, Attempts: reports[:n]}, &assigned); err != nil {
			return err
		}
		reports = reports[n:]
	}
	return completionErr
}
func RunCLI(args []string) error {
	switch args[0] {
	case "next":
		return next(args[1:], os.Stdout)
	case "rotate":
		return rotate()
	case "pair":
		return pair(args[1:])
	case "register":
		return register(args[1:])
	case "unpair":
		_, err := journal("BEGIN IMMEDIATE;DELETE FROM mappings;DELETE FROM profile;COMMIT;")
		if err == nil {
			fmt.Println("Local enrollment removed. Revoke the computer in Devices to invalidate the server credential.")
		}
		return err
	case "status":
		p, err := getProfile()
		if err != nil {
			return err
		}
		fmt.Println("Service", p.Server)
		fmt.Println("Device", p.Enrollment.DeviceID)
		for _, pr := range p.Enrollment.Projects {
			fmt.Println(pr.ID, pr.Repository)
		}
		return nil
	case "sync":
		if len(args) == 1 {
			return syncOnce(context.Background())
		}
		if len(args) != 2 || args[1] != "--watch" {
			return errors.New("use sync [--watch]")
		}
		delay := 5 * time.Second
		for {
			if err := syncOnce(context.Background()); err != nil {
				fmt.Fprintln(os.Stderr, err)
				delay = min(delay*2, time.Minute)
			} else {
				delay = 25 * time.Second
			}
			time.Sleep(delay)
		}
	}
	return errors.New("unknown device command")
}
