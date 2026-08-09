package managedwrite

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/journal"
	"github.com/ontopix/engram/internal/treeimage"
)

type preservationProof struct {
	paths        []journal.PathUpdate
	fingerprints []journal.Fingerprint
	cleanBase    treeimage.Image
}

func provePreservation(ctx context.Context, git *gitClient, observation *repositoryObservation, initial, final treeimage.Image) (*preservationProof, error) {
	if unsupported := unsupportedImageTransitions(initial, final); len(unsupported) != 0 {
		return nil, typedPaths(FailureCapability, PhaseProven, unsupported, fmt.Errorf("%w: candidate requires an unjournalizable file/directory transition or an empty directory", ErrCapability))
	}
	changed := changedImagePaths(initial, final)
	proof := &preservationProof{}
	if len(changed) != 0 {
		updates, directories, conflicts, err := capturePathUpdates(observation.repository.Root, initial, final, changed)
		if err != nil {
			return nil, err
		}
		if len(conflicts) != 0 {
			return nil, typedPaths(FailureConcurrency, PhaseProven, conflicts, ErrConcurrent)
		}
		proof.paths = updates
		for _, update := range updates {
			fingerprint, err := capturePathIdentityFingerprint(observation.repository.Root, update.Path)
			if err != nil {
				return nil, err
			}
			proof.fingerprints = append(proof.fingerprints, fingerprint)
		}
		for _, name := range directories {
			fingerprint, err := captureDirectoryFingerprint(observation.repository.Root, name)
			if err != nil {
				return nil, err
			}
			proof.fingerprints = append(proof.fingerprints, fingerprint)
		}
	}
	presentation, err := capturePresentation(ctx, git, observation, initial, final)
	if err != nil {
		return nil, err
	}
	proof.fingerprints, err = mergeFingerprints(append(proof.fingerprints, presentation...))
	if err != nil {
		return nil, err
	}
	sort.Slice(proof.paths, func(i, j int) bool { return proof.paths[i].Path < proof.paths[j].Path })
	sort.Slice(proof.fingerprints, func(i, j int) bool { return proof.fingerprints[i].Name < proof.fingerprints[j].Name })
	return proof, nil
}

func unsupportedImageTransitions(initial, final treeimage.Image) []string {
	unsupported := make([]string, 0)
	for name, before := range initial {
		after, exists := final[name]
		if exists && before.Kind != after.Kind && (before.Kind == treeimage.Directory || after.Kind == treeimage.Directory) {
			unsupported = append(unsupported, name)
		}
	}
	for name, entry := range final {
		if entry.Kind != treeimage.Directory {
			continue
		}
		prefix := name + "/"
		represented := false
		for child, childEntry := range final {
			if strings.HasPrefix(child, prefix) && childEntry.Kind != treeimage.Directory {
				represented = true
				break
			}
		}
		if !represented {
			unsupported = append(unsupported, name)
		}
	}
	return compactSorted(unsupported)
}

func changedImagePaths(initial, final treeimage.Image) []string {
	set := make(map[string]struct{}, len(initial)+len(final))
	for name := range initial {
		set[name] = struct{}{}
	}
	for name := range final {
		set[name] = struct{}{}
	}
	result := make([]string, 0)
	for name := range set {
		left, leftOK := initial[name]
		right, rightOK := final[name]
		if leftOK != rightOK || leftOK && (left.Kind != right.Kind || !bytes.Equal(left.Data, right.Data)) {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func capturePathUpdates(root string, initial, final treeimage.Image, changed []string) ([]journal.PathUpdate, []string, []string, error) {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rootHandle.Close()
	planned := make(map[string]bool)
	for _, name := range changed {
		planned[name] = true
		for ancestor := filepath.ToSlash(filepath.Dir(filepath.FromSlash(name))); ancestor != "."; ancestor = filepath.ToSlash(filepath.Dir(filepath.FromSlash(ancestor))) {
			if _, exists := planned[ancestor]; !exists {
				planned[ancestor] = false
			}
		}
	}
	ordered := make([]string, 0, len(planned))
	for name := range planned {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)

	updates := make([]journal.PathUpdate, 0, len(ordered))
	directories := []string{"."}
	conflicts := make([]string, 0)
	for _, name := range ordered {
		current, err := observePathRoot(rootHandle, name)
		if err != nil {
			return nil, nil, nil, err
		}
		if !planned[name] {
			if current == nil || current.Kind != string(treeimage.Directory) {
				conflicts = append(conflicts, name)
				continue
			}
			copy := cloneJournalImage(current)
			updates = append(updates, journal.PathUpdate{Path: name, Before: current, After: copy})
			directories = append(directories, name)
			continue
		}

		initialEntry, initialOK := initial[name]
		finalEntry, finalOK := final[name]
		matchesInitial := journalMatchesTree(current, initialEntry, initialOK)
		matchesFinal := journalMatchesTree(current, finalEntry, finalOK)
		if !matchesInitial && !matchesFinal {
			conflicts = append(conflicts, name)
			continue
		}
		before := cloneJournalImage(current)
		var after *journal.Image
		if matchesFinal {
			after = cloneJournalImage(current)
		} else if finalOK {
			after = treeToJournal(finalEntry)
			if current != nil && current.Kind == after.Kind && (after.Kind == "regular" || after.Kind == "directory") {
				after.Mode = current.Mode
			}
		}
		updates = append(updates, journal.PathUpdate{Path: name, Before: before, After: after})
		if current != nil && current.Kind == "directory" || after != nil && after.Kind == "directory" {
			directories = append(directories, name)
		}
	}

	if len(conflicts) == 0 {
		for _, update := range updates {
			if update.After != nil || update.Before == nil || update.Before.Kind != "directory" {
				continue
			}
			directory, _, err := openExactDirectory(rootHandle, update.Path)
			if err != nil {
				return nil, nil, nil, err
			}
			file, err := directory.Open(".")
			if err != nil {
				_ = directory.Close()
				return nil, nil, nil, err
			}
			entries, readErr := file.ReadDir(-1)
			err = errors.Join(readErr, file.Close(), directory.Close())
			if err != nil {
				return nil, nil, nil, err
			}
			for _, entry := range entries {
				child := update.Path + "/" + entry.Name()
				childUpdate := findPathUpdate(updates, child)
				if childUpdate == nil || childUpdate.After != nil {
					conflicts = append(conflicts, child)
				}
			}
		}
	}
	sort.Strings(conflicts)
	directories = compactSorted(directories)
	return updates, directories, conflicts, nil
}

func findPathUpdate(updates []journal.PathUpdate, name string) *journal.PathUpdate {
	index := sort.Search(len(updates), func(index int) bool { return updates[index].Path >= name })
	if index < len(updates) && updates[index].Path == name {
		return &updates[index]
	}
	return nil
}

func observePath(root, logical string) (*journal.Image, error) {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer rootHandle.Close()
	return observePathRoot(rootHandle, logical)
}

func observePathRoot(root *os.Root, logical string) (*journal.Image, error) {
	parent, base, err := openLogicalParent(root, logical)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	before, err := parent.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	image := &journal.Image{Mode: uint32(before.Mode().Perm())}
	switch {
	case before.Mode()&os.ModeSymlink != 0:
		image.Kind = "symlink"
		target, err := parent.Readlink(base)
		if err != nil {
			return nil, err
		}
		image.Data = []byte(target)
		after, err := parent.Lstat(base)
		if err != nil || after.Mode()&os.ModeSymlink == 0 || !os.SameFile(before, after) {
			return nil, fmt.Errorf("worktree path %q changed while being read", logical)
		}
	case before.IsDir():
		image.Kind = "directory"
		directory, err := parent.OpenRoot(base)
		if err != nil {
			return nil, err
		}
		opened, openErr := directory.Stat(".")
		after, statErr := parent.Lstat(base)
		closeErr := directory.Close()
		if openErr != nil || statErr != nil || closeErr != nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
			return nil, errors.Join(openErr, statErr, closeErr, fmt.Errorf("worktree directory %q changed while being observed", logical))
		}
	case before.Mode().IsRegular():
		image.Kind = "regular"
		file, err := parent.OpenFile(base, os.O_RDONLY, 0)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(file)
		after, statErr := parent.Lstat(base)
		opened, openErr := file.Stat()
		closeErr := file.Close()
		if readErr != nil || statErr != nil || openErr != nil || closeErr != nil || !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, after) || after.Mode().Perm() != before.Mode().Perm() {
			return nil, fmt.Errorf("worktree path %q changed while being read", logical)
		}
		image.Data = data
	default:
		return nil, fmt.Errorf("worktree path %q has an unsupported special kind", logical)
	}
	return image, nil
}

func journalMatchesTree(observed *journal.Image, expected treeimage.Entry, present bool) bool {
	if !present {
		return observed == nil
	}
	if observed == nil || observed.Kind != string(expected.Kind) {
		return false
	}
	return expected.Kind == treeimage.Directory || bytes.Equal(observed.Data, expected.Data)
}

func treeToJournal(entry treeimage.Entry) *journal.Image {
	return &journal.Image{Kind: string(entry.Kind), Mode: uint32(entry.Mode.Perm()), Data: append([]byte(nil), entry.Data...)}
}

func cloneJournalImage(image *journal.Image) *journal.Image {
	if image == nil {
		return nil
	}
	copy := *image
	copy.Data = append([]byte(nil), image.Data...)
	return &copy
}

func sameJournalImage(left, right *journal.Image) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.Kind != right.Kind || !bytes.Equal(left.Data, right.Data) {
		return false
	}
	// Appendix B journals exact path existence, kind, and bytes. Directory
	// permission bits are presentation evidence, not a reconciled tree image;
	// treating an umask-restricted newly created directory as an intermediate
	// third image would make portable crash recovery impossible.
	return left.Kind == "directory" || left.Mode == right.Mode
}

func captureDirectoryFingerprint(root, logical string) (journal.Fingerprint, error) {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return journal.Fingerprint{}, err
	}
	defer rootHandle.Close()
	directory, info, err := openExactDirectory(rootHandle, logical)
	if errors.Is(err, os.ErrNotExist) {
		return journal.Fingerprint{Name: "directory:" + encodeName(logical)}, nil
	}
	if err != nil {
		return journal.Fingerprint{}, err
	}
	defer directory.Close()
	file, err := directory.Open(".")
	if err != nil {
		return journal.Fingerprint{}, err
	}
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return journal.Fingerprint{}, errors.Join(readErr, closeErr)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	identityFile, err := directory.Open(".")
	if err != nil {
		return journal.Fingerprint{}, err
	}
	afterInfo, statErr := directory.Stat(".")
	if statErr != nil {
		_ = identityFile.Close()
		return journal.Fingerprint{}, statErr
	}
	identity, identityErr := stableOpenedIdentity(identityFile, info, afterInfo, logical)
	closeErr = identityFile.Close()
	if statErr != nil || identityErr != nil || closeErr != nil {
		return journal.Fingerprint{}, errors.Join(statErr, identityErr, closeErr)
	}
	var data bytes.Buffer
	writeFrame(&data, []byte(identity))
	var directoryMode [4]byte
	binary.BigEndian.PutUint32(directoryMode[:], uint32(info.Mode()))
	writeFrame(&data, directoryMode[:])
	for _, entry := range entries {
		info, err := directory.Lstat(entry.Name())
		if err != nil {
			return journal.Fingerprint{}, err
		}
		writeFrame(&data, []byte(entry.Name()))
		var mode [4]byte
		binary.BigEndian.PutUint32(mode[:], uint32(info.Mode()))
		writeFrame(&data, mode[:])
	}
	return journal.Fingerprint{Name: "directory:" + encodeName(logical), Present: true, Kind: "listing", Data: data.Bytes()}, nil
}

func capturePresentation(ctx context.Context, git *gitClient, observation *repositoryObservation, images ...treeimage.Image) ([]journal.Fingerprint, error) {
	pathsSet := make(map[string]struct{})
	for _, image := range images {
		for name, entry := range image {
			if entry.Kind == treeimage.Regular {
				pathsSet[name] = struct{}{}
			}
		}
	}
	paths := make([]string, 0, len(pathsSet))
	for name := range pathsSet {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	pathBytes := nulJoin(paths)

	fingerprints := []journal.Fingerprint{
		{Name: "environment:presentation", Present: true, Kind: "allowlisted-names", Data: presentationEnvironment()},
		{Name: "head:bytes", Present: true, Kind: "bytes", Data: append([]byte(nil), observation.headBytes...)},
		{Name: "index:presence", Present: observation.indexExists, Kind: presenceKind(observation.indexExists)},
		{Name: "presentation:paths", Present: true, Kind: "nul-paths", Data: pathBytes},
		{Name: "repository:root", Present: true, Kind: "path", Data: []byte(observation.repository.Root)},
	}

	config, err := git.require(ctx, nil, nil, "config", "--null", "--show-origin", "--includes", "--list")
	if err != nil {
		return nil, fmt.Errorf("capture presentation config: %w", err)
	}
	fingerprints = append(fingerprints, journal.Fingerprint{Name: "presentation:config-output", Present: true, Kind: "bytes", Data: config})
	configSources, err := parseConfigSources(config, observation.repository.Root)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"core.autocrlf", "core.sparsecheckout", "index.sparse"} {
		result, err := git.run(ctx, nil, nil, "config", "--type=bool", "--get", key)
		if err != nil {
			return nil, err
		}
		data := append(append(append([]byte(nil), result.stdout...), 0), result.stderr...)
		fingerprints = append(fingerprints, journal.Fingerprint{Name: "presentation:bool:" + key, Present: true, Kind: fmt.Sprintf("status-%d", result.status), Data: data})
		switch result.status {
		case 0:
			value := strings.TrimSuffix(string(result.stdout), "\n")
			if value != "false" {
				return nil, fmt.Errorf("presentation configuration %s must be false or absent", key)
			}
		case 1:
		default:
			return nil, fmt.Errorf("presentation configuration %s is not a valid boolean", key)
		}
	}

	attributeInput := append([]byte(nil), pathBytes...)
	attributeArgs := []string{"check-attr", "-z", "--stdin", "text", "eol", "filter", "ident", "working-tree-encoding"}
	attributeResult, err := git.run(ctx, attributeInput, nil, attributeArgs...)
	if err != nil || attributeResult.status != 0 {
		return nil, fmt.Errorf("capture presentation attributes: %w: %s", err, strings.TrimSpace(string(attributeResult.stderr)))
	}
	if err := validateAttributeOutput(paths, attributeResult.stdout); err != nil {
		return nil, err
	}
	attributeData := append(append(append([]byte(nil), attributeResult.stdout...), 0), attributeResult.stderr...)
	fingerprints = append(fingerprints, journal.Fingerprint{Name: "presentation:attributes-output", Present: true, Kind: "stdout-nul-stderr", Data: attributeData})

	sources := make(map[string]struct{})
	for _, name := range configSources {
		sources[name] = struct{}{}
	}
	sources[filepath.Join(observation.repository.CommonGitDir, "info", "attributes")] = struct{}{}
	for _, name := range paths {
		directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(name)))
		for {
			attributes := filepath.Join(observation.repository.Root, filepath.FromSlash(directory), ".gitattributes")
			if directory == "." {
				attributes = filepath.Join(observation.repository.Root, ".gitattributes")
			}
			sources[attributes] = struct{}{}
			if directory == "." {
				break
			}
			directory = filepath.ToSlash(filepath.Dir(filepath.FromSlash(directory)))
		}
	}
	directories, err := auditPathSpelling(observation.repository.Root, paths)
	if err != nil {
		return nil, err
	}
	for _, logical := range directories {
		fingerprint, err := captureDirectoryFingerprint(observation.repository.Root, logical)
		if err != nil {
			return nil, err
		}
		fingerprints = append(fingerprints, fingerprint)
	}
	attributesFile, err := git.run(ctx, nil, nil, "config", "--path", "--get", "core.attributesFile")
	if err != nil {
		return nil, err
	}
	attributesValue := append(append([]byte(nil), attributesFile.stdout...), attributesFile.stderr...)
	fingerprints = append(fingerprints, journal.Fingerprint{Name: "presentation:attributes-file-query", Present: true, Kind: fmt.Sprintf("status-%d", attributesFile.status), Data: attributesValue})
	if attributesFile.status == 0 {
		name := strings.TrimSuffix(string(attributesFile.stdout), "\n")
		if !filepath.IsAbs(name) {
			name = filepath.Join(observation.repository.Root, name)
		}
		sources[filepath.Clean(name)] = struct{}{}
	} else if attributesFile.status == 1 {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			sources[filepath.Join(xdg, "git", "attributes")] = struct{}{}
		} else if home := os.Getenv("HOME"); home != "" {
			sources[filepath.Join(home, ".config", "git", "attributes")] = struct{}{}
		}
	} else {
		return nil, fmt.Errorf("inspect core.attributesFile exited %d", attributesFile.status)
	}

	orderedSources := make([]string, 0, len(sources))
	for name := range sources {
		orderedSources = append(orderedSources, filepath.Clean(name))
	}
	sort.Strings(orderedSources)
	for _, name := range orderedSources {
		fingerprint, err := captureSourceFingerprint(name)
		if err != nil {
			return nil, err
		}
		fingerprints = append(fingerprints, fingerprint)
	}
	sort.Slice(fingerprints, func(i, j int) bool { return fingerprints[i].Name < fingerprints[j].Name })
	return fingerprints, nil
}

func parseConfigSources(output []byte, root string) ([]string, error) {
	fields := bytes.Split(output, []byte{0})
	if len(fields) != 0 && len(fields[len(fields)-1]) == 0 {
		fields = fields[:len(fields)-1]
	}
	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("malformed --show-origin configuration output")
	}
	set := make(map[string]struct{})
	for index := 0; index < len(fields); index += 2 {
		origin := string(fields[index])
		if !strings.HasPrefix(origin, "file:") {
			continue
		}
		name := strings.TrimPrefix(origin, "file:")
		if !filepath.IsAbs(name) {
			name = filepath.Join(root, name)
		}
		set[filepath.Clean(name)] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for name := range set {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func captureSourceFingerprint(name string) (journal.Fingerprint, error) {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return journal.Fingerprint{Name: "source:" + encodeName(name)}, nil
	}
	if err != nil {
		return journal.Fingerprint{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return journal.Fingerprint{}, fmt.Errorf("presentation source %q is not a real regular file", name)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return journal.Fingerprint{}, err
	}
	after, err := os.Lstat(name)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(info, after) {
		return journal.Fingerprint{}, fmt.Errorf("presentation source %q changed while being read", name)
	}
	identity, err := physicalIdentity(name, info)
	if err != nil {
		return journal.Fingerprint{}, err
	}
	var framed bytes.Buffer
	writeFrame(&framed, []byte(identity))
	var mode [4]byte
	binary.BigEndian.PutUint32(mode[:], uint32(info.Mode()))
	writeFrame(&framed, mode[:])
	writeFrame(&framed, data)
	return journal.Fingerprint{Name: "source:" + encodeName(name), Present: true, Kind: "regular", Data: framed.Bytes()}, nil
}

func capturePathIdentityFingerprint(root, logical string) (journal.Fingerprint, error) {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return journal.Fingerprint{}, err
	}
	defer rootHandle.Close()
	parent, base, err := openLogicalParent(rootHandle, logical)
	if errors.Is(err, os.ErrNotExist) {
		return journal.Fingerprint{Name: "path-identity:" + encodeName(logical)}, nil
	}
	if err != nil {
		return journal.Fingerprint{}, err
	}
	defer parent.Close()
	info, err := parent.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		return journal.Fingerprint{Name: "path-identity:" + encodeName(logical)}, nil
	}
	if err != nil {
		return journal.Fingerprint{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Symlinks have no portable no-follow handle identity. Their exact target
		// remains covered by the path image and ancestor-directory identity.
		target, err := parent.Readlink(base)
		if err != nil {
			return journal.Fingerprint{}, err
		}
		after, err := parent.Lstat(base)
		if err != nil || after.Mode()&os.ModeSymlink == 0 || !os.SameFile(info, after) {
			return journal.Fingerprint{}, fmt.Errorf("filesystem object %q changed while its identity was captured", logical)
		}
		return journal.Fingerprint{Name: "path-identity:" + encodeName(logical), Present: true, Kind: "symlink", Data: []byte(target)}, nil
	}
	var file *os.File
	if info.IsDir() {
		directory, openErr := parent.OpenRoot(base)
		if openErr != nil {
			return journal.Fingerprint{}, openErr
		}
		file, err = directory.Open(".")
		_ = directory.Close()
	} else if info.Mode().IsRegular() {
		file, err = parent.OpenFile(base, os.O_RDONLY, 0)
	} else {
		return journal.Fingerprint{}, fmt.Errorf("filesystem object %q has unsupported kind", logical)
	}
	if err != nil {
		return journal.Fingerprint{}, err
	}
	after, statErr := parent.Lstat(base)
	if statErr != nil {
		_ = file.Close()
		return journal.Fingerprint{}, statErr
	}
	identity, identityErr := stableOpenedIdentity(file, info, after, logical)
	closeErr := file.Close()
	if identityErr != nil || closeErr != nil {
		return journal.Fingerprint{}, errors.Join(identityErr, closeErr)
	}
	return journal.Fingerprint{Name: "path-identity:" + encodeName(logical), Present: true, Kind: "physical", Data: []byte(identity)}, nil
}

func physicalIdentity(name string, before os.FileInfo) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", err
	}
	after, err := os.Lstat(name)
	if err != nil || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return "", fmt.Errorf("filesystem object %q changed while its identity was captured", name)
	}
	identity, ok := persistentFileID(file, opened)
	if !ok {
		return "", fmt.Errorf("persistent filesystem identity is unavailable for %q", name)
	}
	return identity, nil
}

func recaptureFingerprint(ctx context.Context, git *gitClient, root string, expected journal.Fingerprint, all []journal.Fingerprint) (journal.Fingerprint, error) {
	switch {
	case strings.HasPrefix(expected.Name, "directory:"):
		logical, err := decodeName(strings.TrimPrefix(expected.Name, "directory:"))
		if err != nil {
			return journal.Fingerprint{}, err
		}
		return captureDirectoryFingerprint(root, logical)
	case strings.HasPrefix(expected.Name, "source:"):
		name, err := decodeName(strings.TrimPrefix(expected.Name, "source:"))
		if err != nil {
			return journal.Fingerprint{}, err
		}
		return captureSourceFingerprint(name)
	case expected.Name == "environment:presentation":
		return journal.Fingerprint{Name: expected.Name, Present: true, Kind: "allowlisted-names", Data: presentationEnvironment()}, nil
	case strings.HasPrefix(expected.Name, "path-identity:"):
		logical, err := decodeName(strings.TrimPrefix(expected.Name, "path-identity:"))
		if err != nil {
			return journal.Fingerprint{}, err
		}
		return capturePathIdentityFingerprint(root, logical)
	case expected.Name == "presentation:config-output":
		output, err := git.require(ctx, nil, nil, "config", "--null", "--show-origin", "--includes", "--list")
		return journal.Fingerprint{Name: expected.Name, Present: true, Kind: "bytes", Data: output}, err
	case strings.HasPrefix(expected.Name, "presentation:bool:"):
		key := strings.TrimPrefix(expected.Name, "presentation:bool:")
		result, err := git.run(ctx, nil, nil, "config", "--type=bool", "--get", key)
		if err != nil {
			return journal.Fingerprint{}, err
		}
		data := append(append(append([]byte(nil), result.stdout...), 0), result.stderr...)
		return journal.Fingerprint{Name: expected.Name, Present: true, Kind: fmt.Sprintf("status-%d", result.status), Data: data}, nil
	case expected.Name == "presentation:attributes-output":
		paths := fingerprintByName(all, "presentation:paths")
		if paths == nil {
			return journal.Fingerprint{}, fmt.Errorf("presentation path fingerprint is missing")
		}
		result, err := git.run(ctx, paths.Data, nil, "check-attr", "-z", "--stdin", "text", "eol", "filter", "ident", "working-tree-encoding")
		if err != nil || result.status != 0 {
			return journal.Fingerprint{}, fmt.Errorf("recapture attributes: %w", err)
		}
		data := append(append(append([]byte(nil), result.stdout...), 0), result.stderr...)
		return journal.Fingerprint{Name: expected.Name, Present: true, Kind: "stdout-nul-stderr", Data: data}, nil
	case expected.Name == "presentation:attributes-file-query":
		result, err := git.run(ctx, nil, nil, "config", "--path", "--get", "core.attributesFile")
		if err != nil {
			return journal.Fingerprint{}, err
		}
		data := append(append([]byte(nil), result.stdout...), result.stderr...)
		return journal.Fingerprint{Name: expected.Name, Present: true, Kind: fmt.Sprintf("status-%d", result.status), Data: data}, nil
	case expected.Name == "presentation:paths", expected.Name == "index:presence":
		return expected, nil
	case expected.Name == "repository:root":
		return journal.Fingerprint{Name: expected.Name, Present: true, Kind: "path", Data: []byte(root)}, nil
	case expected.Name == "head:bytes":
		repository, err := gitrawDiscoverForFingerprint(ctx, root)
		if err != nil {
			return journal.Fingerprint{}, err
		}
		data, err := readRealFile(filepath.Join(repository.GitDir, "HEAD"))
		return journal.Fingerprint{Name: expected.Name, Present: true, Kind: "bytes", Data: data}, err
	default:
		return journal.Fingerprint{}, fmt.Errorf("unknown managed safety fingerprint %q", expected.Name)
	}
}

// Indirection keeps proof.go independent of ref interpretation details while
// still making HEAD bytes recoverable from only a store root.
var gitrawDiscoverForFingerprint = func(ctx context.Context, root string) (*gitraw.Repository, error) {
	return gitraw.Discover(ctx, root)
}

func fingerprintByName(values []journal.Fingerprint, name string) *journal.Fingerprint {
	for index := range values {
		if values[index].Name == name {
			return &values[index]
		}
	}
	return nil
}

func sameFingerprint(left, right journal.Fingerprint) bool {
	return left.Name == right.Name && left.Present == right.Present && left.Kind == right.Kind && bytes.Equal(left.Data, right.Data)
}

func presenceKind(present bool) string {
	if present {
		return "present"
	}
	return ""
}

func writeFrame(buffer *bytes.Buffer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	buffer.Write(length[:])
	buffer.Write(value)
}

func nulJoin(values []string) []byte {
	var result bytes.Buffer
	for _, value := range values {
		result.WriteString(value)
		result.WriteByte(0)
	}
	return result.Bytes()
}

func encodeName(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeName(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return string(decoded), err
}

func compactSorted(values []string) []string {
	sort.Strings(values)
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func mergeFingerprints(values []journal.Fingerprint) ([]journal.Fingerprint, error) {
	byName := make(map[string]journal.Fingerprint, len(values))
	for _, value := range values {
		if existing, exists := byName[value.Name]; exists {
			if !sameFingerprint(existing, value) {
				return nil, fmt.Errorf("safety input %q produced inconsistent observations", value.Name)
			}
			continue
		}
		byName[value.Name] = value
	}
	result := make([]journal.Fingerprint, 0, len(byName))
	for _, value := range byName {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func validateAttributeOutput(paths []string, output []byte) error {
	fields := bytes.Split(output, []byte{0})
	if len(fields) != 0 && len(fields[len(fields)-1]) == 0 {
		fields = fields[:len(fields)-1]
	}
	const attributes = 5
	if len(fields) != len(paths)*attributes*3 {
		return fmt.Errorf("Git returned an incomplete presentation attribute result")
	}
	allowed := map[string]bool{"text": true, "eol": true, "filter": true, "ident": true, "working-tree-encoding": true}
	seen := make(map[string]map[string]bool, len(paths))
	for offset := 0; offset < len(fields); offset += 3 {
		name, attribute, value := string(fields[offset]), string(fields[offset+1]), string(fields[offset+2])
		index := sort.SearchStrings(paths, name)
		if index == len(paths) || paths[index] != name || !allowed[attribute] {
			return fmt.Errorf("Git returned an unexpected presentation attribute record")
		}
		if seen[name] == nil {
			seen[name] = make(map[string]bool)
		}
		if seen[name][attribute] {
			return fmt.Errorf("Git returned a duplicate presentation attribute record")
		}
		seen[name][attribute] = true
		admitted := value == "unspecified" || attribute == "text" && value == "unset"
		if !admitted {
			return fmt.Errorf("presentation attribute %s=%s transforms %s", attribute, value, name)
		}
	}
	return nil
}

func auditPathSpelling(root string, paths []string) ([]string, error) {
	directories := []string{"."}
	for _, logical := range paths {
		directory := root
		logicalDirectory := "."
		components := strings.Split(logical, "/")
		for index, component := range components {
			entries, err := os.ReadDir(directory)
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			if err != nil {
				return nil, err
			}
			found := false
			for _, entry := range entries {
				if entry.Name() == component {
					found = true
					break
				}
			}
			next := filepath.Join(directory, component)
			if !found {
				if _, err := os.Lstat(next); err == nil {
					return nil, fmt.Errorf("filesystem does not preserve exact pathname bytes for %s", logical)
				} else if !errors.Is(err, os.ErrNotExist) {
					return nil, err
				}
				break
			}
			if index == len(components)-1 {
				break
			}
			info, err := os.Lstat(next)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("logical ancestor of %s is not a real directory", logical)
			}
			directory = next
			if logicalDirectory == "." {
				logicalDirectory = component
			} else {
				logicalDirectory += "/" + component
			}
			directories = append(directories, logicalDirectory)
		}
	}
	return compactSorted(directories), nil
}
