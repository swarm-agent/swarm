//go:build !linux

package tool

import (
	"errors"
	"io/fs"
	"os"
)

func (p *rootedWorkspacePath) openLocalMutationParent(bool) (*os.File, string, error) {
	return nil, "", errors.New("Bubblewrap file mutation requires Linux")
}
func (p *rootedWorkspacePath) openLocalMutationFile(int, fs.FileMode) (*os.File, error) {
	return nil, errors.New("Bubblewrap file mutation requires Linux")
}
