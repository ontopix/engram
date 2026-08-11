package acquire

import "github.com/ontopix/engram/internal/fsatomic"

func renameNoReplace(oldPath, newPath string) (bool, error) {
	return fsatomic.RenameNoReplace(oldPath, newPath)
}
