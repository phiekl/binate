// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package landlock

import (
	"fmt"
	"path/filepath"

	golandlock "github.com/landlock-lsm/go-landlock/landlock"
	llsyscall "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

// Policy lists what the restricted process may read: the directories
// compared, whose contents it may list and read, and the files compared
// on their own, through a rule on their directory, which lets it read any
// file beneath that directory, at any depth, but list none.
// Inputs opened before restricting, such as pipes, need no entry.
type Policy struct {
	Dirs  []string
	Files []string
}

// Rules returns the rules of p, placed on resolved paths, since Landlock
// decides access by where a file really is: a directory given through a
// symlink gets its rule on the symlink's target, and a file gets its rule
// on the directory its resolved path is in. A rule on a file's directory,
// unlike one on the file, still covers the file after an editor replaces
// it by renaming a new file over it.
func (p Policy) Rules() ([]golandlock.Rule, error) {
	var rules []golandlock.Rule
	for _, dir := range p.Dirs {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, fmt.Errorf("resolving %s: %w", dir, err)
		}
		rules = append(rules, golandlock.PathAccess(
			llsyscall.AccessFSReadFile|llsyscall.AccessFSReadDir, resolved))
	}
	for _, file := range p.Files {
		resolved, err := filepath.EvalSymlinks(file)
		if err != nil {
			return nil, fmt.Errorf("resolving %s: %w", file, err)
		}
		rules = append(rules, golandlock.PathAccess(
			llsyscall.AccessFSReadFile, filepath.Dir(resolved)))
	}
	return rules, nil
}
