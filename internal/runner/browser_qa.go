// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lucasmkrx/bugsnitch/internal/protocol"
)

type BrowserQACheck struct {
	Assertion string   `json:"assertion"`
	Command   []string `json:"command"`
}

func trustedProjectTool(checkout, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("the command must use an absolute executable path")
	}
	bin, err := canonical(path)
	if err != nil {
		return "", errors.New("the executable is unavailable")
	}
	if bin == checkout || strings.HasPrefix(bin, checkout+string(filepath.Separator)) {
		return "", errors.New("the executable must be installed outside the project checkout")
	}
	info, err := os.Stat(bin)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", errors.New("the configured file must be an executable regular file")
	}
	return bin, nil
}

func configureBrowserQA(checkout string, checks map[int]BrowserQACheck) error {
	if len(checks) > 100 {
		return errors.New("configure at most 100 browser checks per project")
	}
	for issue, check := range checks {
		if issue < 1 || issue > 2147483647 || !protocol.BrowserQAText(check.Assertion) || len(check.Command) < 1 || len(check.Command) > 32 {
			return errors.New("configure each browser QA check with an issue number, a short assertion and an argument vector")
		}
		for _, arg := range check.Command {
			if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
				return errors.New("invalid browser QA command argument")
			}
		}
		bin, err := trustedProjectTool(checkout, check.Command[0])
		if err != nil {
			return fmt.Errorf("browser QA for issue #%d: %w", issue, err)
		}
		check.Command[0] = bin
	}
	return nil
}

// CaptureBrowserQA runs the operator-configured check against two immutable
// Git exports. No application checkout or browser output is shared by this command.
func CaptureBrowserQA(ctx context.Context, id string) (protocol.BrowserQAReport, error) {
	var report protocol.BrowserQAReport
	a, err := Get(id)
	if err != nil {
		return report, err
	}
	if a.BaseSHA == "" || a.CandidateSHA == "" || a.BaseSHA == a.CandidateSHA {
		return report, errors.New("select an attempt with a committed candidate")
	}
	p, err := project(a.Workspace, a.Repository)
	if err != nil {
		return report, err
	}
	check, ok := p.Config.BrowserQA[a.IssueNumber]
	if !ok {
		return report, fmt.Errorf("configure browserQA for issue #%d with project-add", a.IssueNumber)
	}
	if err := checkOrigin(p.Path, a.Repository); err != nil {
		return report, err
	}
	if len(check.Command) < 1 {
		return report, errors.New("register a browser QA executable with project-add")
	}
	bin, err := trustedProjectTool(p.Path, check.Command[0])
	if err != nil || bin != check.Command[0] {
		return report, errors.New("browser QA executable changed or is unavailable; check the configuration and run project-add again")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	before, err := captureBrowserRevision(ctx, p.Path, a.BaseSHA, check)
	if err != nil {
		return report, fmt.Errorf("browser QA baseline: %w", err)
	}
	after, err := captureBrowserRevision(ctx, p.Path, a.CandidateSHA, check)
	if err != nil {
		return report, fmt.Errorf("browser QA candidate: %w", err)
	}
	current, err := Get(id)
	if err != nil || current.BaseSHA != a.BaseSHA || current.CandidateSHA != a.CandidateSHA {
		return report, errors.New("attempt changed during the browser check; inspect the current candidate and run QA again")
	}
	return protocol.BrowserQAReport{Protocol: protocol.Version, AttemptID: a.ID, IssueNumber: a.IssueNumber,
		Assertion: check.Assertion, CheckedAt: time.Now().UTC(), Before: before, After: after}, nil
}

func captureBrowserRevision(ctx context.Context, checkout, revision string, check BrowserQACheck) (protocol.BrowserQAObservation, error) {
	var observation protocol.BrowserQAObservation
	dir, err := os.MkdirTemp("", "bugsnitch-browser-qa-")
	if err != nil {
		return observation, err
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "source")
	if err = os.Mkdir(source, 0700); err != nil {
		return observation, err
	}
	if err := exportRevision(ctx, checkout, source, revision); err != nil {
		return observation, err
	}
	input, err := json.Marshal(map[string]string{"checkout": source, "revision": revision, "assertion": check.Assertion})
	if err != nil {
		return observation, err
	}
	cmd := exec.Command(check.Command[0], check.Command[1:]...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + dir, "TMPDIR=" + dir, "PATH=/usr/bin:/bin", "LC_ALL=C"}
	cmd.Stdin = bytes.NewReader(append(input, '\n'))
	output := capped{max: protocol.BrowserQAWireLimit / 2}
	cmd.Stdout = &output
	cmd.WaitDelay = 2 * time.Second
	err = runCheckGroup(ctx, cmd)
	if ctx.Err() != nil {
		return observation, ctx.Err()
	}
	if err != nil {
		return observation, fmt.Errorf("configured browser check did not finish successfully: %w", err)
	}
	decoder := json.NewDecoder(&output.buffer)
	decoder.DisallowUnknownFields()
	if output.overflow || decoder.Decode(&observation) != nil || decoder.Decode(new(any)) != io.EOF {
		return observation, errors.New("browser check must return one bounded JSON observation")
	}
	return observation, observation.Validate(revision)
}

func exportRevision(ctx context.Context, checkout, destination, revision string) error {
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	// Start from an exact object, without using the checkout's modified files,
	// local Git replacement objects, smudge filters or repository hooks.
	if len(revision) != 40 && len(revision) != 64 || strings.Trim(revision, "0123456789abcdef") != "" {
		return errors.New("invalid Git revision for source export")
	}
	cmd := exec.CommandContext(ctx, "git", "--no-replace-objects", "-C", checkout, "archive", "--format=tar", revision)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer stdout.Close()
	if err = cmd.Start(); err != nil {
		return err
	}
	limit := &io.LimitedReader{R: stdout, N: 512 << 20}
	extractErr := extractRevision(root, tar.NewReader(limit))
	if extractErr == nil {
		_, extractErr = io.Copy(io.Discard, limit)
	}
	_ = stdout.Close()
	waitErr := cmd.Wait()
	if limit.N == 0 {
		return errors.New("Git export is larger than 512 MiB")
	}
	if err := errors.Join(extractErr, waitErr); err != nil {
		return fmt.Errorf("could not export the selected Git revision: %w", err)
	}
	return nil
}

func extractRevision(root *os.Root, files *tar.Reader) error {
	for {
		entry, err := files.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := strings.TrimSuffix(entry.Name, "/")
		if !filepath.IsLocal(name) || filepath.Clean(name) != name || name == "." {
			return errors.New("unsafe path in Git export")
		}
		switch entry.Typeflag {
		case tar.TypeDir:
			err = root.Mkdir(name, 0700)
		case tar.TypeReg:
			var f *os.File
			f, err = root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600|os.FileMode(entry.Mode)&0100)
			if err == nil {
				_, copyErr := io.Copy(f, files)
				err = errors.Join(copyErr, f.Close())
			}
		case tar.TypeSymlink:
			if entry.Linkname == "" || filepath.IsAbs(entry.Linkname) || !filepath.IsLocal(filepath.Join(filepath.Dir(name), entry.Linkname)) {
				return errors.New("Git export contains a link outside the snapshot")
			}
			err = root.Symlink(entry.Linkname, name)
		default:
			return errors.New("unsupported Git export entry")
		}
		if err != nil {
			return err
		}
	}
}
