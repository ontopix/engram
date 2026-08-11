// Package lockidentity gives cooperating lock files an ownership identity
// which remains unambiguous after their creating descriptor is closed.
package lockidentity

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

const randomBytes = 32

// State describes what a stable observation found at a lock path.
type State uint8

const (
	Absent State = iota
	Owned
	Other
)

// Identity combines the filesystem object created by an owner with a random
// token stored in that object. File identity alone is insufficient: once the
// creating descriptor is closed, some filesystems can immediately reuse its
// inode for a successor lock.
type Identity struct {
	info  os.FileInfo
	token []byte
}

// Establish writes a fresh ownership token to a newly and exclusively created
// lock file and captures its filesystem identity.
func Establish(file *os.File) (Identity, error) {
	if file == nil {
		return Identity{}, errors.New("lock file is nil")
	}
	random := make([]byte, randomBytes)
	if _, err := rand.Read(random); err != nil {
		return Identity{}, fmt.Errorf("generate lock ownership token: %w", err)
	}
	token := make([]byte, hex.EncodedLen(len(random))+1)
	hex.Encode(token, random)
	token[len(token)-1] = '\n'
	written, writeErr := file.Write(token)
	if writeErr == nil && written != len(token) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return Identity{}, fmt.Errorf("write lock ownership token: %w", writeErr)
	}
	info, err := file.Stat()
	if err != nil {
		return Identity{}, fmt.Errorf("stat owned lock: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Identity{}, errors.New("owned lock is not a regular file")
	}
	return Identity{info: info, token: token}, nil
}

// Inspect observes name through an opened descriptor and two name snapshots.
// Owned is returned only when both the filesystem object and the token still
// belong to this identity. A successor which happens to reuse the old inode is
// therefore classified as Other.
func (identity Identity) Inspect(name string) (State, error) {
	if identity.info == nil || len(identity.token) == 0 {
		return Other, errors.New("lock ownership identity is unavailable")
	}
	before, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return Absent, nil
	}
	if err != nil {
		return Other, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return Other, nil
	}
	file, err := os.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return Absent, nil
	}
	if err != nil {
		return Other, err
	}
	openedBefore, statBeforeErr := file.Stat()
	raw, readErr := io.ReadAll(io.LimitReader(file, int64(len(identity.token)+1)))
	openedAfter, statAfterErr := file.Stat()
	closeErr := file.Close()
	after, lstatErr := os.Lstat(name)
	if errors.Is(lstatErr, os.ErrNotExist) {
		return Absent, errors.Join(statBeforeErr, readErr, statAfterErr, closeErr)
	}
	if err := errors.Join(statBeforeErr, readErr, statAfterErr, closeErr, lstatErr); err != nil {
		return Other, err
	}
	if openedBefore == nil || openedAfter == nil || after == nil ||
		!openedBefore.Mode().IsRegular() || !openedAfter.Mode().IsRegular() ||
		after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!os.SameFile(before, openedBefore) || !os.SameFile(openedBefore, openedAfter) ||
		!os.SameFile(openedAfter, after) {
		return Other, nil
	}
	if os.SameFile(identity.info, after) && bytes.Equal(raw, identity.token) {
		return Owned, nil
	}
	return Other, nil
}
