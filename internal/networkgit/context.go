// Package networkgit constructs disposable Git contexts for authorized
// repository-network operations. The context deliberately does not read the
// managed repository's local configuration, so a concurrent url.* rewrite
// cannot redirect an already selected endpoint.
package networkgit

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/ontopix/engram/internal/gitraw"
)

// Context is a private bare repository whose object reads fall through to the
// managed repository. Network writes land in its private object directory and
// are promoted explicitly only after the remote advertisement is stabilized.
type Context struct {
	root          string
	localObjects  string
	objectHexWide int
}

// New creates a private bare repository using the managed repository's object
// format and object store as a read-only alternate.
func New(commonGitDir string, format gitraw.ObjectFormat) (*Context, error) {
	objects := filepath.Join(commonGitDir, "objects")
	info, err := os.Lstat(objects)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.Join(err, errors.New("managed object store is not one real directory"))
	}
	objects, err = filepath.Abs(objects)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "engram-network-git-")
	if err != nil {
		return nil, err
	}
	result := &Context{root: root, localObjects: objects, objectHexWide: format.HexWidth()}
	if err := result.initialize(format); err != nil {
		return nil, errors.Join(err, os.RemoveAll(root))
	}
	return result, nil
}

func (c *Context) initialize(format gitraw.ObjectFormat) error {
	for _, name := range []string{
		filepath.Join(c.root, "objects", "info"), filepath.Join(c.root, "objects", "pack"),
		filepath.Join(c.root, "refs", "heads"), filepath.Join(c.root, "refs", "tags"),
	} {
		if err := os.MkdirAll(name, 0o700); err != nil {
			return err
		}
	}
	configuration := "[core]\n\trepositoryformatversion = 0\n\tbare = true\n"
	if format == gitraw.SHA256 {
		configuration = "[core]\n\trepositoryformatversion = 1\n\tbare = true\n[extensions]\n\tobjectformat = sha256\n"
	} else if format != gitraw.SHA1 {
		return errors.New("unsupported private network object format")
	}
	if err := writeExclusive(filepath.Join(c.root, "config"), []byte(configuration)); err != nil {
		return err
	}
	if err := writeExclusive(filepath.Join(c.root, "HEAD"), []byte("ref: refs/heads/main\n")); err != nil {
		return err
	}
	// Git accepts a C-style quoted absolute path in objects/info/alternates.
	// strconv.Quote keeps newlines, backslashes, and non-ASCII path bytes from
	// changing the one-line alternates grammar.
	alternate := strconv.Quote(c.localObjects) + "\n"
	return writeExclusive(filepath.Join(c.root, "objects", "info", "alternates"), []byte(alternate))
}

func writeExclusive(name string, data []byte) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

// Root is the private bare repository root passed to Git's -C option.
func (c *Context) Root() string {
	if c == nil {
		return ""
	}
	return c.root
}

// Close removes the disposable repository. It never removes or rewrites the
// managed object store.
func (c *Context) Close() error {
	if c == nil || c.root == "" {
		return nil
	}
	root := c.root
	c.root = ""
	return os.RemoveAll(root)
}

// Promote atomically publishes only fetched object files into the managed
// object store. Refs and configuration remain private. Existing object files
// must be byte-identical and are never overwritten.
func (c *Context) Promote() error {
	if c == nil || c.root == "" || (c.objectHexWide != 40 && c.objectHexWide != 64) {
		return errors.New("private network object context is unavailable")
	}
	privateObjects := filepath.Join(c.root, "objects")
	entries, err := os.ReadDir(privateObjects)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case name == "info":
			continue
		case name == "pack":
			if err := c.promotePacks(filepath.Join(privateObjects, name)); err != nil {
				return err
			}
		case validHex(name, 2):
			if err := c.promoteLooseDirectory(filepath.Join(privateObjects, name), name); err != nil {
				return err
			}
		default:
			return fmt.Errorf("private network object store contains unsupported entry %q", name)
		}
	}
	return syncDirectory(c.localObjects)
}

func (c *Context) promoteLooseDirectory(source, prefix string) error {
	info, err := os.Lstat(source)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.Join(err, fmt.Errorf("private loose-object directory %q is unsafe", prefix))
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	destination := filepath.Join(c.localObjects, prefix)
	if err := requireRealDirectory(destination); err != nil {
		return err
	}
	for _, entry := range entries {
		if !validHex(entry.Name(), c.objectHexWide-2) || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("private loose-object path %q is unsafe", prefix+"/"+entry.Name())
		}
		if err := publishExactFile(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return syncDirectory(destination)
}

func (c *Context) promotePacks(source string) error {
	info, err := os.Lstat(source)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.Join(err, errors.New("private pack directory is unsafe"))
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	files := make(map[string]map[string]string)
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("private pack entry %q is unsafe", entry.Name())
		}
		extension := filepath.Ext(entry.Name())
		base := strings.TrimSuffix(entry.Name(), extension)
		if !strings.HasPrefix(base, "pack-") || !validHex(strings.TrimPrefix(base, "pack-"), c.objectHexWide) ||
			(extension != ".pack" && extension != ".idx" && extension != ".rev" && extension != ".bitmap") {
			return fmt.Errorf("private pack entry %q is unsupported", entry.Name())
		}
		if files[base] == nil {
			files[base] = make(map[string]string)
		}
		files[base][extension] = entry.Name()
	}
	bases := make([]string, 0, len(files))
	for base, set := range files {
		if set[".pack"] == "" || set[".idx"] == "" {
			return fmt.Errorf("private pack %q is incomplete", base)
		}
		bases = append(bases, base)
	}
	sort.Strings(bases)
	destination := filepath.Join(c.localObjects, "pack")
	if err := requireRealDirectory(destination); err != nil {
		return err
	}
	for _, base := range bases {
		// Publish data before its index. Git ignores an unindexed pack, so
		// concurrent readers never select a partially copied object set.
		for _, extension := range []string{".pack", ".idx", ".rev", ".bitmap"} {
			name := files[base][extension]
			if name == "" {
				continue
			}
			if err := publishExactFile(filepath.Join(source, name), filepath.Join(destination, name)); err != nil {
				return err
			}
		}
	}
	return syncDirectory(destination)
}

func requireRealDirectory(name string) error {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		info, err = os.Lstat(name)
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.Join(err, fmt.Errorf("object destination %q is not one real directory", name))
	}
	return nil
}

func publishExactFile(source, destination string) error {
	before, err := os.Lstat(source)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return errors.Join(err, fmt.Errorf("object source %q is not one real file", source))
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	opened, statErr := input.Stat()
	if statErr != nil || !os.SameFile(before, opened) {
		_ = input.Close()
		return errors.Join(statErr, errors.New("object source changed before promotion"))
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".engram-object-")
	if err != nil {
		_ = input.Close()
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	copyBytes, copyErr := io.Copy(temporary, input)
	syncErr := temporary.Sync()
	closeOutputErr := temporary.Close()
	closeInputErr := input.Close()
	after, afterErr := os.Lstat(source)
	if copyErr != nil || syncErr != nil || closeOutputErr != nil || closeInputErr != nil || afterErr != nil ||
		copyBytes != before.Size() || !os.SameFile(before, after) {
		return errors.Join(copyErr, syncErr, closeOutputErr, closeInputErr, afterErr, errors.New("object source changed during promotion"))
	}
	if err := os.Link(temporaryName, destination); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		equal, compareErr := equalRegularFiles(temporaryName, destination)
		if compareErr != nil || !equal {
			return errors.Join(compareErr, errors.New("existing object destination differs"))
		}
	}
	return nil
}

func equalRegularFiles(left, right string) (bool, error) {
	rightInfo, err := os.Lstat(right)
	if err != nil || rightInfo.Mode()&os.ModeSymlink != 0 || !rightInfo.Mode().IsRegular() {
		return false, errors.Join(err, errors.New("existing object destination is unsafe"))
	}
	leftFile, err := os.Open(left)
	if err != nil {
		return false, err
	}
	defer leftFile.Close()
	rightFile, err := os.Open(right)
	if err != nil {
		return false, err
	}
	defer rightFile.Close()
	leftBuffer, rightBuffer := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		leftCount, leftErr := io.ReadFull(leftFile, leftBuffer)
		rightCount, rightErr := io.ReadFull(rightFile, rightBuffer)
		if leftCount != rightCount || !equalBytes(leftBuffer[:leftCount], rightBuffer[:rightCount]) {
			return false, nil
		}
		if errors.Is(leftErr, io.EOF) || errors.Is(leftErr, io.ErrUnexpectedEOF) || errors.Is(rightErr, io.EOF) || errors.Is(rightErr, io.ErrUnexpectedEOF) {
			return leftErr == rightErr || (errors.Is(leftErr, io.EOF) && errors.Is(rightErr, io.EOF)) ||
				(errors.Is(leftErr, io.ErrUnexpectedEOF) && errors.Is(rightErr, io.ErrUnexpectedEOF)), nil
		}
		if leftErr != nil || rightErr != nil {
			return false, errors.Join(leftErr, rightErr)
		}
	}
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func syncDirectory(name string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
