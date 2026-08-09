package conformance

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrPathNotRepresentable reports that the host filesystem aliases two
// distinct manifest path spellings. Callers may treat it as a platform
// capability limitation; the harness never silently overwrites the alias.
var ErrPathNotRepresentable = errors.New("filesystem cannot represent exact fixture path")

// MaterializedCase names the independently materialized states for one case.
// Snapshot is set for snapshot cases. Base and Candidate are set for
// changesets, except that an unavailable base has an empty Base path and
// BaseUnavailable set.
type MaterializedCase struct {
	Snapshot        string
	Base            string
	Candidate       string
	BaseUnavailable bool
}

// ValidateReferences verifies that Seed is a real directory and every
// write_text source is a real regular file below repoRoot. No symbolic-link
// component is followed.
func (m *Manifest) ValidateReferences(repoRoot string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	repository, err := openRealRoot(repoRoot)
	if err != nil {
		return fmt.Errorf("open repository root: %w", err)
	}
	defer repository.Close()

	if err := requireRealDirectory(repository, m.Seed); err != nil {
		return fmt.Errorf("seed %q: %w", m.Seed, err)
	}
	checkOperations := func(where string, operations []Operation) error {
		for i := range operations {
			operation := operations[i]
			if operation.Kind != OperationWriteText {
				continue
			}
			if err := requireRealRegular(repository, *operation.Source); err != nil {
				return fmt.Errorf("%s[%d].source %q: %w", where, i, *operation.Source, err)
			}
		}
		return nil
	}
	if err := checkOperations("common", m.Common); err != nil {
		return err
	}
	for i := range m.Cases {
		c := &m.Cases[i]
		prefix := fmt.Sprintf("cases[%d]", i)
		if c.Snapshot != nil {
			if err := checkOperations(prefix+".snapshot.operations", c.Snapshot.Operations); err != nil {
				return err
			}
		}
		if c.Base != nil && !c.Base.Unavailable {
			if err := checkOperations(prefix+".base.operations", c.Base.Operations); err != nil {
				return err
			}
		}
		if c.Candidate != nil {
			if err := checkOperations(prefix+".candidate.operations", c.Candidate.Operations); err != nil {
				return err
			}
		}
	}
	return nil
}

// MaterializeCase creates one case below tempRoot. Callers should pass a fresh
// testing.T.TempDir (or an equivalently private empty directory). Each state
// starts as an independent byte-for-byte copy of Seed, receives Common, then
// its state-specific operations.
func (m *Manifest) MaterializeCase(repoRoot, tempRoot, caseID string) (*MaterializedCase, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	c, ok := m.CaseByID(caseID)
	if !ok {
		return nil, fmt.Errorf("unknown conformance case %q", caseID)
	}
	repository, err := openRealRoot(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("open repository root: %w", err)
	}
	defer repository.Close()
	if err := requireRealDirectory(repository, m.Seed); err != nil {
		return nil, fmt.Errorf("seed %q: %w", m.Seed, err)
	}

	temporary, err := openRealRoot(tempRoot)
	if err != nil {
		return nil, fmt.Errorf("open temporary root: %w", err)
	}
	defer temporary.Close()

	result := &MaterializedCase{}
	switch c.Kind {
	case KindSnapshot:
		result.Snapshot, err = materializeState(repository, temporary, m.Seed, "snapshot", m.Common, c.Snapshot.Operations)
	case KindChangeset:
		if c.Base.Unavailable {
			result.BaseUnavailable = true
		} else {
			result.Base, err = materializeState(repository, temporary, m.Seed, "base", m.Common, c.Base.Operations)
		}
		if err == nil {
			result.Candidate, err = materializeState(repository, temporary, m.Seed, "candidate", m.Common, c.Candidate.Operations)
		}
	default:
		return nil, fmt.Errorf("case %q has invalid kind %q", c.ID, c.Kind)
	}
	if err != nil {
		return nil, fmt.Errorf("materialize case %q: %w", c.ID, err)
	}
	return result, nil
}

func materializeState(repository, temporary *os.Root, seed, stateName string, common, specific []Operation) (string, error) {
	if err := mkdirExact(temporary, stateName, 0o700); err != nil {
		return "", fmt.Errorf("create %s state: %w", stateName, err)
	}
	stateRoot, err := temporary.OpenRoot(stateName)
	if err != nil {
		return "", fmt.Errorf("open %s state: %w", stateName, err)
	}
	defer stateRoot.Close()
	if err := copySeed(repository, seed, stateRoot); err != nil {
		return "", err
	}
	if err := applyOperations(repository, stateRoot, common); err != nil {
		return "", fmt.Errorf("apply common operations: %w", err)
	}
	if err := applyOperations(repository, stateRoot, specific); err != nil {
		return "", fmt.Errorf("apply state operations: %w", err)
	}
	return filepath.Join(temporary.Name(), filepath.FromSlash(stateName)), nil
}

func copySeed(repository *os.Root, seed string, destination *os.Root) error {
	if err := requireRealDirectory(repository, seed); err != nil {
		return fmt.Errorf("inspect seed: %w", err)
	}
	return copyDirectoryContents(repository, seed, destination, ".")
}

func copyDirectoryContents(source *os.Root, sourceDir string, destination *os.Root, destinationDir string) error {
	directory, err := source.Open(filepath.FromSlash(sourceDir))
	if err != nil {
		return fmt.Errorf("open seed directory %q: %w", sourceDir, err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return fmt.Errorf("read seed directory %q: %w", sourceDir, readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close seed directory %q: %w", sourceDir, closeErr)
	}

	for _, entry := range entries {
		sourceName := path.Join(sourceDir, entry.Name())
		destinationName := entry.Name()
		if destinationDir != "." {
			destinationName = path.Join(destinationDir, entry.Name())
		}
		info, statErr := source.Lstat(filepath.FromSlash(sourceName))
		if statErr != nil {
			return fmt.Errorf("inspect seed entry %q: %w", sourceName, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("seed entry %q is a symbolic link", sourceName)
		}
		switch {
		case info.IsDir():
			if err := mkdirExact(destination, destinationName, 0o755); err != nil {
				return fmt.Errorf("create seed directory %q: %w", destinationName, err)
			}
			if err := copyDirectoryContents(source, sourceName, destination, destinationName); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			data, err := readRealRegular(source, sourceName)
			if err != nil {
				return fmt.Errorf("read seed file %q: %w", sourceName, err)
			}
			if err := writeRegular(destination, destinationName, data, info.Mode().Perm()); err != nil {
				return fmt.Errorf("write seed file %q: %w", destinationName, err)
			}
		default:
			return fmt.Errorf("seed entry %q is not a real file or directory", sourceName)
		}
	}
	return nil
}

func applyOperations(repository, destination *os.Root, operations []Operation) error {
	for i := range operations {
		operation := &operations[i]
		var err error
		switch operation.Kind {
		case OperationWriteText:
			var data []byte
			data, err = readRealRegular(repository, *operation.Source)
			if err == nil {
				err = writeRegular(destination, operation.Path, data, 0o644)
			}
		case OperationWriteBase64:
			var data []byte
			data, err = base64.StdEncoding.Strict().DecodeString(*operation.Content)
			if err == nil {
				err = writeRegular(destination, operation.Path, data, 0o644)
			}
		case OperationRemove:
			err = removeRegular(destination, operation.Path)
		default:
			err = fmt.Errorf("unknown operation %q", operation.Kind)
		}
		if err != nil {
			return fmt.Errorf("operation %d (%s %q): %w", i, operation.Kind, operation.Path, err)
		}
	}
	return nil
}

func openRealRoot(name string) (*os.Root, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%q is a symbolic link", name)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", name)
	}
	return os.OpenRoot(name)
}

func requireRealDirectory(root *os.Root, name string) error {
	if err := requireNoSymlinkComponents(root, name); err != nil {
		return err
	}
	info, err := root.Lstat(filepath.FromSlash(name))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("is not a real directory")
	}
	return nil
}

func requireRealRegular(root *os.Root, name string) error {
	if err := requireNoSymlinkComponents(root, name); err != nil {
		return err
	}
	info, err := root.Lstat(filepath.FromSlash(name))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("is not a real regular file")
	}
	return nil
}

func readRealRegular(root *os.Root, name string) ([]byte, error) {
	if err := requireRealRegular(root, name); err != nil {
		return nil, err
	}
	file, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func writeRegular(root *os.Root, name string, data []byte, mode fs.FileMode) error {
	if err := makeRealParents(root, path.Dir(name)); err != nil {
		return err
	}
	localName := filepath.FromSlash(name)
	info, err := lstatExact(root, name)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination %q is a symbolic link", name)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("destination %q is not a regular file", name)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	file, err := root.OpenFile(localName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	if closeErr != nil {
		return closeErr
	}
	_, err = lstatExact(root, name)
	return err
}

func removeRegular(root *os.Root, name string) error {
	if err := requireRealRegular(root, name); err != nil {
		return err
	}
	return root.Remove(filepath.FromSlash(name))
}

func makeRealParents(root *os.Root, directory string) error {
	if directory == "." {
		return nil
	}
	current := ""
	for _, component := range strings.Split(directory, "/") {
		if current == "" {
			current = component
		} else {
			current = path.Join(current, component)
		}
		localName := filepath.FromSlash(current)
		info, err := lstatExact(root, current)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := root.Mkdir(localName, 0o755); err != nil {
				return fmt.Errorf("create parent %q: %w", current, err)
			}
			if _, err := lstatExact(root, current); err != nil {
				return err
			}
		case err != nil:
			return err
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("parent %q is a symbolic link", current)
		case !info.IsDir():
			return fmt.Errorf("parent %q is not a directory", current)
		}
	}
	return nil
}

func requireNoSymlinkComponents(root *os.Root, name string) error {
	current := ""
	components := strings.Split(filepath.ToSlash(name), "/")
	for i, component := range components {
		if current == "" {
			current = component
		} else {
			current = path.Join(current, component)
		}
		info, err := lstatExact(root, current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component %q is a symbolic link", current)
		}
		if i < len(components)-1 && !info.IsDir() {
			return fmt.Errorf("path component %q is not a directory", current)
		}
	}
	return nil
}

func lstatExact(root *os.Root, name string) (fs.FileInfo, error) {
	localName := filepath.FromSlash(name)
	info, err := root.Lstat(localName)
	if err != nil {
		return nil, err
	}
	parent, base := path.Split(filepath.ToSlash(name))
	parent = strings.TrimSuffix(parent, "/")
	if parent == "" {
		parent = "."
	}
	directory, err := root.Open(filepath.FromSlash(parent))
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	for _, entry := range entries {
		if entry.Name() == base {
			return info, nil
		}
	}
	return nil, fmt.Errorf("%w: %q aliases a differently spelled entry", ErrPathNotRepresentable, name)
}

func mkdirExact(root *os.Root, name string, mode fs.FileMode) error {
	_, err := lstatExact(root, name)
	switch {
	case err == nil:
		return fmt.Errorf("path %q already exists", name)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if err := root.Mkdir(filepath.FromSlash(name), mode); err != nil {
		return err
	}
	_, err = lstatExact(root, name)
	return err
}
