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
// compared, and those of the repositories, whose contents it may list and
// read. Files opened before restricting, such as the terminal, need no
// entry.
type Policy struct {
	Dirs []string
}

// Rules returns the rules of p, placed on resolved paths, since Landlock
// decides access by where a file really is: a directory given through a
// symlink gets its rule on the symlink's target. A file an editor replaces
// by renaming a new file over it is still beneath its directory's rule.
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
	return rules, nil
}
