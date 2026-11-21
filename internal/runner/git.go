// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type capped struct {
	bytes.Buffer
	max      int
	overflow bool
}

func (b *capped) Write(data []byte) (int, error) {
	n := len(data)
	space := b.max - b.Len()
	if len(data) > space {
		data = data[:space]
		b.overflow = true
	}
	_, err := b.Buffer.Write(data)
	return n, err
}
func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "user.name=Bugsnitch", "-c", "user.email=runner@bugsnitch.invalid", "-C", dir}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1", "LC_ALL=C"}
	var out capped
	out.max = 512 << 10
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s failed", args[0])
	}
	if out.overflow {
		return "", errors.New("Git output exceeds artifact limit")
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}
func canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
func checkOrigin(root, repo string) error {
	top, err := git(root, "rev-parse", "--show-toplevel")
	if err != nil || root != top {
		return errors.New("mapping must identify the canonical checkout root")
	}
	remote, err := git(root, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	remote = strings.TrimSuffix(strings.ToLower(remote), ".git")
	for _, p := range []string{"git@github.com:", "https://github.com/", "ssh://git@github.com/"} {
		if p+strings.ToLower(repo) == remote {
			return nil
		}
	}
	return errors.New("registered path and selected GitHub repository disagree")
}
func safeRelative(s string) bool {
	if s == "" || !filepath.IsLocal(s) || filepath.ToSlash(filepath.Clean(s)) != s || s == "." || strings.ContainsAny(s, "\n\r\x00\\:") {
		return false
	}
	for _, p := range strings.Split(s, "/") {
		if strings.HasPrefix(p, ".") || p == ".." {
			return false
		}
	}
	return true
}
func readFile(root, rel string) (string, error) {
	if !safeRelative(rel) {
		return "", errors.New("approved file path invalid")
	}
	p := filepath.Join(root, rel)
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil || p != resolved {
		return "", errors.New("approved file missing or reached through symlink")
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", errors.New("approved path must be a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(b) > 65536 || !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return "", errors.New("approved file must be UTF-8 text under 64 KiB")
	}
	return string(b), nil
}
func (a *Attempt) checkLocation() error {
	dir, err := home()
	if err != nil {
		return err
	}
	parent := filepath.Join(dir, "worktrees")
	if !idPattern.MatchString(a.ID) || a.Branch != "bugsnitch/"+a.ID || a.Worktree != filepath.Join(parent, a.ID) {
		return errors.New("attempt location differs from registered identity")
	}
	if err = os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	p, err := canonical(parent)
	if err != nil || p != parent {
		return errors.New("worktree parent changed")
	}
	return nil
}
func (a *Attempt) checkTree() error {
	if err := a.checkLocation(); err != nil {
		return err
	}
	projectRoot, err := canonical(a.Project.Path)
	if err != nil || projectRoot != a.Project.Path {
		return errors.New("registered checkout moved")
	}
	if err := checkOrigin(projectRoot, a.Repository); err != nil {
		return err
	}
	expected, err := canonical(a.Worktree)
	if err != nil || expected != a.Worktree || expected == projectRoot {
		return errors.New("attempt worktree unavailable")
	}
	top, err := git(expected, "rev-parse", "--show-toplevel")
	if err != nil || top != expected {
		return errors.New("worktree root mismatch")
	}
	branch, err := git(expected, "symbolic-ref", "--short", "HEAD")
	if err != nil || branch != a.Branch {
		return errors.New("worktree branch mismatch")
	}
	common, err := git(expected, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	repoCommon, err := git(projectRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common != repoCommon {
		return errors.New("worktree association mismatch")
	}
	return nil
}
func (a *Attempt) prepare() error {
	if err := a.checkLocation(); err != nil {
		return err
	}
	if err := checkOrigin(a.Project.Path, a.Repository); err != nil {
		return err
	}
	if _, err := os.Lstat(a.Worktree); errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(filepath.Dir(a.Worktree), 0700); err != nil {
			return err
		}
		if _, err = git(a.Project.Path, "worktree", "add", "-b", a.Branch, "--", a.Worktree, a.BaseSHA); err != nil {
			return err
		}
	}
	if err := a.checkTree(); err != nil {
		return err
	}
	sha, err := git(a.Worktree, "rev-parse", "HEAD")
	if err != nil || sha != a.BaseSHA {
		return errors.New("worktree has diverged from the recorded base")
	}
	return nil
}
func (a *Attempt) contextFiles() (map[string]string, error) {
	files := map[string]string{}
	total := 0
	for _, p := range a.Project.Config.Files {
		tracked, err := git(a.Worktree, "ls-files", "--error-unmatch", "--", p)
		if err != nil || tracked != p {
			return nil, errors.New("approved source must be tracked in the recorded revision")
		}
		text, err := readFile(a.Worktree, p)
		if err != nil {
			return nil, err
		}
		total += len(text)
		if total > 128<<10 {
			return nil, errors.New("approved context exceeds 128 KiB")
		}
		files[p] = text
	}
	return files, nil
}
func AddProject(w, r, path string, config Config) error {
	r = strings.ToLower(r)
	if !workspacePattern.MatchString(w) || !repoPattern.MatchString(r) {
		return errors.New("valid workspace and owner/repository required")
	}
	dir, err := canonical(path)
	if err != nil {
		return err
	}
	if err = checkOrigin(dir, r); err != nil {
		return err
	}
	state, err := home()
	if err != nil {
		return err
	}
	if state == dir || strings.HasPrefix(state, dir+string(filepath.Separator)) || strings.HasPrefix(dir, state+string(filepath.Separator)) {
		return errors.New("runner state and repository must use separate directories")
	}
	if config.ValidationTimeoutSeconds < 0 || config.ValidationTimeoutSeconds > 14400 {
		return errors.New("validation timeout must be within four hours")
	}
	for stage, settings := range config.Stages {
		if stage != "plan" && stage != "implement" && stage != "review" {
			return errors.New("unknown model stage")
		}
		if settings.TimeoutSeconds < 0 || settings.TimeoutSeconds > 3600 {
			return errors.New("model stage timeout must be within one hour")
		}
	}
	if len(config.Files) == 0 || len(config.Files) > 32 || len(config.Validate) == 0 || len(config.Validate) > 48 {
		return errors.New("choose 1–32 tracked files and a validation argument vector")
	}
	if !filepath.IsAbs(config.Validate[0]) {
		return errors.New("validation executable must be an absolute path")
	}
	bin, err := canonical(config.Validate[0])
	if err != nil || strings.HasPrefix(bin, dir+string(filepath.Separator)) {
		return errors.New("validation executable must be installed outside checkout")
	}
	info, err := os.Stat(bin)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return errors.New("validation executable unavailable")
	}
	config.Validate[0] = bin
	for _, name := range config.Files {
		if _, err := readFile(dir, name); err != nil {
			return err
		}
	}
	_, err = query("INSERT INTO local_projects VALUES(" + literal(w) + "," + literal(r) + "," + packed(Project{w, r, dir, config}) + ") ON CONFLICT(workspace,repo) DO UPDATE SET data=excluded.data;")
	return err
}
