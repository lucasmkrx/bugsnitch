// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func (c Config) editPolicy() string {
	if len(c.EditDirectories) > 0 {
		return "local-reviewed-directories/v1"
	}
	return "local-reviewed-files/v1"
}

func (c Config) inDirectory(path string) bool {
	return safeRelative(path) && slices.ContainsFunc(c.EditDirectories, func(dir string) bool {
		return safeRelative(dir) && strings.HasPrefix(path, dir+"/")
	})
}

func (c Config) allowsFile(path string) bool {
	return safeRelative(path) && (slices.Contains(c.Files, path) || c.inDirectory(path))
}

// Only regular tracked files enter the model context. Directory registrations
// may name paths which a new feature will create for the first time.
func (a *Attempt) discoverFiles() ([]string, error) {
	c := a.Project.Config
	if len(c.EditDirectories) == 0 {
		return c.Files, nil
	}
	entries, err := git(a.Worktree, append([]string{"ls-files", "--stage", "-z", "--"}, c.EditDirectories...)...)
	if err != nil {
		return nil, err
	}
	names := slices.Clone(c.Files)
	for record := range strings.SplitSeq(strings.TrimSuffix(entries, "\x00"), "\x00") {
		header, path, ok := strings.Cut(record, "\t")
		if !ok || !c.inDirectory(path) || (!strings.HasPrefix(header, "100644 ") && !strings.HasPrefix(header, "100755 ")) {
			continue
		}
		names = append(names, path)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) > 128 {
		return nil, errors.New("more than 128 files in edit context; register narrower directories")
	}
	return names, nil
}

func checkDirectory(root, rel string) error {
	current := root
	if rel == "." {
		return nil
	}
	for part := range strings.SplitSeq(rel, "/") {
		current = filepath.Join(current, part)
		st, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || !st.IsDir() {
			return errors.New("edit directory traverses a non-directory or symlink")
		}
	}
	return nil
}

type fileChange struct {
	path   string
	before string
	after  string
	create bool
	remove bool
	mode   os.FileMode
}

type edit struct{ Operation, Path, Destination, Content string }

var scopedImplementSchema = obj(map[string]any{"summary": str, "edits": arr(obj(map[string]any{
	"operation": map[string]any{"type": "string", "enum": []string{"create", "replace", "rename", "delete"}},
	"path":      str, "destination": str, "content": str,
}))})

func (a *Attempt) fileChanges(files map[string]string, edits []edit) ([]fileChange, error) {
	c := a.Project.Config
	var changes []fileChange
	seen := map[string]bool{}
	size := 0
	if len(edits) == 0 || len(edits) > 128 {
		return nil, errors.New("invalid number of proposed edits")
	}
	for _, e := range edits {
		content, existing := files[e.Path]
		if !c.allowsFile(e.Path) || len(e.Content) > 65536 || strings.ContainsRune(e.Content, '\x00') {
			return nil, errors.New("edit outside approved file contract")
		}
		op := e.Operation
		if len(c.EditDirectories) == 0 {
			if op != "" || e.Destination != "" {
				return nil, errors.New("file-only project accepts replacements only")
			}
			op = "replace"
		}
		if op != "rename" && e.Destination != "" {
			return nil, errors.New("only a rename has a destination")
		}
		for _, path := range []string{e.Path, e.Destination} {
			if path == "" {
				continue
			}
			key := strings.ToLower(path)
			if seen[key] {
				return nil, errors.New("overlapping edits")
			}
			seen[key] = true
		}
		size += len(e.Content)
		if size > 128<<10 && len(c.EditDirectories) > 0 {
			return nil, errors.New("proposed edit content exceeds 128 KiB")
		}
		switch op {
		case "replace":
			if !existing {
				return nil, errors.New("replacement requires an existing approved file")
			}
			if content != e.Content {
				changes = append(changes, fileChange{path: e.Path, before: content, after: e.Content})
			}
		case "delete", "rename":
			if !existing || !c.inDirectory(e.Path) || slices.Contains(c.Files, e.Path) || e.Content != "" {
				return nil, errors.New("deletion or rename requires a discovered file and empty content")
			}
			changes = append(changes, fileChange{path: e.Path, before: content, remove: true})
			if op == "rename" {
				mode, err := os.Lstat(filepath.Join(a.Worktree, e.Path))
				if err != nil || !mode.Mode().IsRegular() {
					return nil, errors.New("rename source changed")
				}
				if err := a.checkNewPath(e.Destination); err != nil {
					return nil, err
				}
				changes = append(changes, fileChange{path: e.Destination, after: content, create: true, mode: mode.Mode().Perm()})
			}
		case "create":
			if err := a.checkNewPath(e.Path); err != nil {
				return nil, err
			}
			changes = append(changes, fileChange{path: e.Path, after: e.Content, create: true, mode: 0644})
		default:
			return nil, errors.New("unknown file operation")
		}
	}
	if len(changes) == 0 {
		return nil, errors.New("worker proposed no change")
	}
	// A path cannot act as both a new file and a parent directory in the batch.
	for path := range seen {
		for parent := filepath.ToSlash(filepath.Dir(path)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if seen[parent] {
				return nil, errors.New("file and directory operations overlap")
			}
		}
	}
	if len(c.EditDirectories) > 0 {
		// A correction uses the resulting files as its next context.
		count, bytes := len(files), 0
		for _, content := range files {
			bytes += len(content)
		}
		for _, change := range changes {
			bytes += len(change.after) - len(change.before)
			if change.create {
				count++
			}
			if change.remove {
				count--
			}
		}
		if count > 128 || bytes > 128<<10 {
			return nil, errors.New("result exceeds the registered discovery context limits")
		}
	}
	return changes, nil
}

func (a *Attempt) checkNewPath(path string) error {
	if !a.Project.Config.inDirectory(path) {
		return errors.New("new file outside registered directories")
	}
	if err := checkDirectory(a.Worktree, filepath.ToSlash(filepath.Dir(path))); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(a.Worktree, path)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("new file would replace an existing path")
	}
	// check-ignore rejects Git's global literal pathspec flag. safeRelative rejects
	// magic prefixes and the option terminator protects leading dashes.
	_, err := git(a.Worktree, "--no-literal-pathspecs", "check-ignore", "--quiet", "--", path)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return errors.New("new file is ignored or its git ignore rules could not be read")
	}
	return nil
}

func intentFor(parent string, changes []fileChange, artifact Artifact) *CandidateIntent {
	intent := &CandidateIntent{Parent: parent, SHA256: map[string]string{}, Modes: map[string]string{}, Artifact: artifact}
	for _, change := range changes {
		digest := ""
		if !change.remove {
			digest = fmt.Sprintf("%x", sha256.Sum256([]byte(change.after)))
		}
		intent.SHA256[change.path] = digest
		if change.create {
			mode := "100644"
			if change.mode&0100 != 0 {
				mode = "100755"
			}
			intent.Modes[change.path] = mode
		}
	}
	return intent
}

func (a *Attempt) applyChanges(changes []fileChange) error {
	root, err := os.OpenRoot(a.Worktree)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, change := range changes {
		if change.create {
			if err := a.checkNewPath(change.path); err != nil {
				return err
			}
			if err := root.MkdirAll(filepath.Dir(change.path), 0755); err != nil {
				return err
			}
		} else {
			current, err := readFile(a.Worktree, change.path)
			if err != nil || current != change.before {
				return errors.New("approved file changed during turn")
			}
		}
		if change.remove {
			if err := root.Remove(change.path); err != nil {
				return err
			}
			continue
		}
		mode := change.mode
		if !change.create {
			st, err := root.Lstat(change.path)
			if err != nil || !st.Mode().IsRegular() {
				return errors.New("replacement source changed")
			}
			mode = st.Mode().Perm()
		}
		// Write a new inode, even for replacements. Link publishes a new file
		// without clobbering an unexpected file created during this turn.
		id, err := newID()
		if err != nil {
			return err
		}
		tmp := filepath.Join(filepath.Dir(change.path), ".bugsnitch-edit-"+id)
		f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if err = f.Chmod(mode); err == nil {
			_, err = f.WriteString(change.after)
		}
		err = errors.Join(err, f.Close())
		if err == nil {
			if change.create {
				err = root.Link(tmp, change.path)
			} else {
				err = root.Rename(tmp, change.path)
			}
		}
		if change.create || err != nil {
			err = errors.Join(err, root.Remove(tmp))
		}
		if err != nil {
			return err
		}
	}
	return nil
}
