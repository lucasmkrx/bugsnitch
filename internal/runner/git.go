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
	buffer   bytes.Buffer
	max      int
	overflow bool
}

func (b *capped) Write(data []byte) (int, error) {
	n := len(data)
	space := b.max - b.buffer.Len()
	if len(data) > space {
		data = data[:space]
		b.overflow = true
	}
	_, err := b.buffer.Write(data)
	return n, err
}
func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--literal-pathspecs", "-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "user.name=Bugsnitch", "-c", "user.email=runner@bugsnitch.invalid", "-C", dir}, args...)...)
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
	return strings.TrimSuffix(out.buffer.String(), "\n"), nil
}

// changedPaths returns Git's literal names, including paths that porcelain quotes.
func changedPaths(dir, base, head string) ([]string, error) {
	names, err := git(dir, "diff", "--no-renames", "--name-only", "-z", base, head, "--")
	if err != nil || names == "" {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(names, "\x00"), "\x00"), nil
}

func canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
func checkOrigin(root, repo string) error {
	return nativeOperation(context.Background(), "--worktree", struct {
		Version    int    `json:"version"`
		Op         string `json:"op"`
		Project    string `json:"project"`
		Repository string `json:"repository"`
	}{1, "origin", root, repo}, nil)
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

// Only registered attempt identity crosses the worktree boundary. Model context,
// device credentials and repository configuration stay in the runner.
type worktreeIdentity struct {
	Home       string `json:"home"`
	Project    string `json:"project"`
	Repository string `json:"repository"`
	ID         string `json:"id"`
	Branch     string `json:"branch"`
	Worktree   string `json:"worktree"`
	Base       string `json:"base"`
}

func (a *Attempt) nativeWorktreeIdentity() (worktreeIdentity, error) {
	dir, err := home()
	if err != nil {
		return worktreeIdentity{}, err
	}
	return worktreeIdentity{dir, a.Project.Path, a.Repository, a.ID, a.Branch, a.Worktree, a.BaseSHA}, nil
}

func (a *Attempt) worktreeOp(op, sha string) error {
	identity, err := a.nativeWorktreeIdentity()
	if err != nil {
		return err
	}
	return nativeOperation(context.Background(), "--worktree", struct {
		Version int              `json:"version"`
		Op      string           `json:"op"`
		Attempt worktreeIdentity `json:"attempt"`
		SHA     string           `json:"sha,omitempty"`
	}{1, op, identity, sha}, nil)
}

func (a *Attempt) checkTree() error         { return a.worktreeOp("inspect", "") }
func (a *Attempt) prepare() error           { return a.worktreeOp("prepare", "") }
func (a *Attempt) cleanAt(sha string) error { return a.worktreeOp("clean", sha) }

func (a *Attempt) contextFiles() (map[string]string, error) {
	files := map[string]string{}
	total := 0
	names, err := a.discoverFiles()
	if err != nil {
		return nil, err
	}
	for _, p := range names {
		tracked, err := git(a.Worktree, "ls-files", "-z", "--error-unmatch", "--", p)
		if err != nil || tracked != p+"\x00" {
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
		switch settings.Provider {
		case "", "codex":
		case "claude":
			if strings.TrimSpace(settings.Model) == "" || len(settings.Model) > 160 {
				return errors.New("a Claude stage requires an explicit model")
			}
			switch settings.Reasoning {
			case "", "low", "medium", "high", "xhigh", "max":
			default:
				return errors.New("unsupported Claude stage effort")
			}
		default:
			return errors.New("unknown model stage provider")
		}
	}
	if len(config.Files)+len(config.EditDirectories) == 0 || len(config.Files) > 32 || len(config.EditDirectories) > 16 || len(config.Validate) == 0 || len(config.Validate) > 48 {
		return errors.New("choose approved files or edit directories and a validation argument vector")
	}
	if config.ReviewDepth != "" && reviewLevel(config.ReviewDepth) == 0 {
		return errors.New("reviewDepth must be quick, standard or deep")
	}
	if config.ValidationSandbox != "" && config.ValidationSandbox != "isolated" {
		return errors.New("unknown validation sandbox")
	}
	for _, scope := range config.EditDirectories {
		if !safeRelative(scope) {
			return errors.New("invalid edit directory")
		}
		if err := checkDirectory(dir, scope); err != nil {
			return err
		}
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
