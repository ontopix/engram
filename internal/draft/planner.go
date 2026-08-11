package draft

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/discovery"
	"github.com/ontopix/engram/internal/fileidentity"
	"github.com/ontopix/engram/internal/snapshot"
)

type planner struct {
	checked *checker.Snapshot
	plan    *Plan
	root    *os.Root
}

func openPlanner(ctx context.Context, operation, rootName string) (*planner, error) {
	if err := cancelled(ctx, operation); err != nil {
		return nil, err
	}
	rootName, err := discovery.Exact(rootName)
	if err != nil {
		return nil, typed(ErrorRepository, operation, ".", err)
	}
	rootInfo, err := os.Lstat(rootName)
	if err != nil {
		return nil, typed(ErrorIO, operation, ".", err)
	}
	if err := fileidentity.Pin(rootInfo); err != nil {
		return nil, typed(ErrorIO, operation, ".", err)
	}
	checked, err := checker.CheckFS(rootName)
	if err != nil {
		var capability *checker.CapabilityError
		if errors.As(err, &capability) {
			return nil, typed(ErrorCapability, operation, ".", err)
		}
		return nil, typed(ErrorIO, operation, ".", err)
	}
	if !regularTreeFile(checked, ".engram/root.yaml", snapshot.RoleRootManifest) || hasErrorFinding(checked, ".engram/root.yaml") {
		return nil, typed(ErrorRepository, operation, ".engram/root.yaml", errors.New("selected root has no usable engram manifest"))
	}
	root, err := os.OpenRoot(rootName)
	if err != nil {
		return nil, typed(ErrorIO, operation, ".", err)
	}
	result := &planner{
		checked: checked, root: root,
		plan: &Plan{root: rootName, rootInfo: rootInfo, operation: operation, captures: make(map[string]observation)},
	}
	manifest := checked.Tree.Files[".engram/root.yaml"]
	if _, err := result.captureFile(".engram/root.yaml", manifest.Data); err != nil {
		result.close()
		return nil, err
	}
	return result, nil
}

func (p *planner) close() {
	if p != nil && p.root != nil {
		_ = p.root.Close()
	}
}

func (p *planner) captureFile(logicalPath string, expected []byte) (observation, error) {
	current, err := observe(p.root, logicalPath, true, false)
	if err != nil {
		return observation{}, typed(ErrorIO, p.plan.operation, logicalPath, err)
	}
	if current.kind != observationRegular || !bytes.Equal(current.data, expected) {
		return observation{}, concurrencyError(p.plan.operation, logicalPath)
	}
	if err := p.mergeCapture(current); err != nil {
		return observation{}, err
	}
	if err := p.captureAncestors(logicalPath); err != nil {
		return observation{}, err
	}
	return current, nil
}

func (p *planner) captureAbsent(logicalPath string) (observation, error) {
	current, err := observe(p.root, logicalPath, false, false)
	if err != nil {
		return observation{}, typed(ErrorIO, p.plan.operation, logicalPath, err)
	}
	if current.kind != observationAbsent {
		return observation{}, typed(ErrorConflict, p.plan.operation, logicalPath, errors.New("destination already exists"))
	}
	if err := p.mergeCapture(current); err != nil {
		return observation{}, err
	}
	if err := p.captureAncestors(logicalPath); err != nil {
		return observation{}, err
	}
	return current, nil
}

func (p *planner) captureDirectory(logicalPath string) (observation, error) {
	current, err := observe(p.root, logicalPath, false, true)
	if err != nil {
		return observation{}, typed(ErrorIO, p.plan.operation, logicalPath, err)
	}
	if current.kind != observationDirectory {
		return observation{}, typed(ErrorRepository, p.plan.operation, logicalPath, errors.New("content boundary is not a real directory"))
	}
	expected := expectedEntries(p.checked.Tree, logicalPath)
	if !sameEntries(expected, current.entries) {
		return observation{}, concurrencyError(p.plan.operation, logicalPath)
	}
	if err := p.mergeCapture(current); err != nil {
		return observation{}, err
	}
	if err := p.captureAncestors(joinLogical(logicalPath, "sentinel")); err != nil {
		return observation{}, err
	}
	return current, nil
}

func (p *planner) captureKind(logicalPath string, wanted observationKind) (observation, error) {
	current, err := observe(p.root, logicalPath, false, false)
	if err != nil {
		return observation{}, typed(ErrorIO, p.plan.operation, logicalPath, err)
	}
	if current.kind != wanted {
		return observation{}, concurrencyError(p.plan.operation, logicalPath)
	}
	if err := p.mergeCapture(current); err != nil {
		return observation{}, err
	}
	return current, nil
}

func (p *planner) captureAncestors(logicalPath string) error {
	for directory := parentLogical(logicalPath); directory != "."; directory = parentLogical(directory) {
		if _, exists := p.plan.captures[directory]; exists {
			continue
		}
		if _, err := p.captureKind(directory, observationDirectory); err != nil {
			return err
		}
	}
	return nil
}

func (p *planner) mergeCapture(value observation) error {
	if existing, ok := p.plan.captures[value.path]; ok {
		// A stronger observation carries file bytes or directory entries. Merge
		// only observations of the same still-live filesystem object.
		if existing.kind != value.kind || existing.mode != value.mode || existing.kind != observationAbsent && !os.SameFile(existing.info, value.info) {
			return concurrencyError(p.plan.operation, value.path)
		}
		if existing.kind == observationRegular {
			switch {
			case existing.data != nil && value.data != nil && !bytes.Equal(existing.data, value.data):
				return concurrencyError(p.plan.operation, value.path)
			case existing.data == nil && value.data != nil:
				existing.data = append([]byte(nil), value.data...)
			}
		}
		if existing.kind == observationDirectory && existing.entries == nil && value.entries != nil {
			existing.entries = append([]observedEntry(nil), value.entries...)
		}
		p.plan.captures[value.path] = existing
		return nil
	}
	value.data = append([]byte(nil), value.data...)
	value.entries = append([]observedEntry(nil), value.entries...)
	p.plan.captures[value.path] = value
	return nil
}

func (p *planner) addFile(logicalPath string, before observation, after []byte, staging string) {
	p.addFileMode(logicalPath, before, after, staging, 0)
}

func (p *planner) addFileMode(logicalPath string, before observation, after []byte, staging string, createMode fs.FileMode) {
	if bytes.Equal(before.data, after) && before.kind == observationRegular {
		return
	}
	mode := fs.FileMode(0o644)
	if before.kind == observationRegular {
		mode = before.mode.Perm()
	} else if createMode.Perm() != 0 {
		mode = createMode.Perm()
	}
	p.plan.files = append(p.plan.files, fileEdit{
		path: logicalPath, before: before, after: append([]byte(nil), after...), mode: mode, staging: staging,
	})
}

func (p *planner) addDelete(logicalPath string, before observation) {
	p.plan.files = append(p.plan.files, fileEdit{
		path: logicalPath, before: before, mode: before.mode.Perm(),
		staging: parentLogical(logicalPath), delete: true,
	})
}

func (p *planner) addDirectory(logicalPath string, mode fs.FileMode) {
	p.plan.dirs = append(p.plan.dirs, directoryEdit{path: logicalPath, mode: mode.Perm()})
}

// captureSchemaRoute captures the exact boundary sequence that makes lexical
// schema resolution available at one content directory.
func (p *planner) captureSchemaRoute(at string) error {
	for scope := at; ; scope = parentLogical(scope) {
		config := joinLogical(scope, ".engram")
		configKind, configExists := p.checked.Tree.Boundaries[config]
		switch {
		case !configExists:
			if _, err := p.captureAbsent(config); err != nil {
				return err
			}
		case configKind != snapshot.KindDirectory:
			return typed(ErrorRepository, p.plan.operation, config, errors.New("schema resolution .engram boundary is not a real directory"))
		default:
			if _, err := p.captureKind(config, observationDirectory); err != nil {
				return err
			}
			schemasDirectory := joinLogical(config, "schemas")
			schemasKind, schemasExists := p.checked.Tree.Boundaries[schemasDirectory]
			switch {
			case !schemasExists:
				if _, err := p.captureAbsent(schemasDirectory); err != nil {
					return err
				}
			case schemasKind != snapshot.KindDirectory:
				return typed(ErrorRepository, p.plan.operation, schemasDirectory, errors.New("schema resolution boundary is not a real directory"))
			default:
				if _, err := p.captureDirectory(schemasDirectory); err != nil {
					return err
				}
			}
		}
		if scope == "." {
			break
		}
	}
	return nil
}

// captureSchemaTopology captures every scope at or below rootScope because a
// newly copied ancestor definition can create forbidden descendant shadowing.
func (p *planner) captureSchemaTopology(rootScope string) error {
	if err := p.captureSchemaRoute(rootScope); err != nil {
		return err
	}
	for _, scope := range p.checked.Tree.Directories {
		if scope != rootScope && !scopeBelow(rootScope, scope) {
			continue
		}
		config := joinLogical(scope, ".engram")
		configKind, configExists := p.checked.Tree.Boundaries[config]
		if !configExists {
			if _, err := p.captureAbsent(config); err != nil {
				return err
			}
			continue
		}
		if configKind != snapshot.KindDirectory {
			return typed(ErrorRepository, p.plan.operation, config, errors.New("descendant .engram boundary is not a real directory"))
		}
		if _, err := p.captureKind(config, observationDirectory); err != nil {
			return err
		}
		schemasDirectory := joinLogical(config, "schemas")
		schemasKind, schemasExists := p.checked.Tree.Boundaries[schemasDirectory]
		if !schemasExists {
			if _, err := p.captureAbsent(schemasDirectory); err != nil {
				return err
			}
			continue
		}
		if schemasKind != snapshot.KindDirectory {
			return typed(ErrorRepository, p.plan.operation, schemasDirectory, errors.New("descendant schema boundary is not a real directory"))
		}
		if _, err := p.captureDirectory(schemasDirectory); err != nil {
			return err
		}
		for _, name := range treeFilePaths(p.checked.Tree, snapshot.RoleSchema, schemasDirectory) {
			file := p.checked.Tree.Files[name]
			if file.Role == snapshot.RoleSchema {
				if _, err := p.captureFile(name, file.Data); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func treeFilePaths(tree *snapshot.Tree, role snapshot.FileRole, directory string) []string {
	result := make([]string, 0)
	if tree == nil {
		return result
	}
	for name, file := range tree.Files {
		if file.Role == role && parentLogical(name) == directory {
			result = append(result, name)
		}
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare([]byte(result[i]), []byte(result[j])) < 0 })
	return result
}

func scopeBelow(scope, candidate string) bool {
	if scope == "." {
		return candidate != "."
	}
	return len(candidate) > len(scope) && strings.HasPrefix(candidate, scope) && candidate[len(scope)] == '/'
}

func expectedEntries(tree *snapshot.Tree, directory string) []observedEntry {
	result := make([]observedEntry, 0)
	if tree == nil {
		return result
	}
	for logicalPath, kind := range tree.Boundaries {
		if parentLogical(logicalPath) == directory {
			result = append(result, observedEntry{name: path.Base(logicalPath), kind: snapshotKind(kind)})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return bytes.Compare([]byte(result[i].name), []byte(result[j].name)) < 0
	})
	return result
}

func sameEntries(left, right []observedEntry) bool {
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

func regularTreeFile(checked *checker.Snapshot, logicalPath string, role snapshot.FileRole) bool {
	if checked == nil || checked.Tree == nil {
		return false
	}
	file, ok := checked.Tree.Files[logicalPath]
	return ok && file.Role == role
}

func hasErrorFinding(checked *checker.Snapshot, logicalPath string) bool {
	if checked == nil {
		return true
	}
	for _, finding := range checked.Validation.Findings {
		if finding.Path == logicalPath && strings.HasPrefix(finding.Code, "E") {
			return true
		}
	}
	return false
}

func validLogicalDirectory(value string) bool {
	if value == "" || value == "." {
		return true
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || path.Clean(value) != value {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if !snapshot.ValidContentName(component) || component == "README.md" {
			return false
		}
	}
	return true
}

func validRecordPath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || path.Clean(value) != value {
		return false
	}
	components := strings.Split(value, "/")
	for _, component := range components {
		if !snapshot.ValidContentName(component) {
			return false
		}
	}
	name := components[len(components)-1]
	return name != "README.md" && strings.HasSuffix(name, ".md")
}

func containsDirectory(directories []string, wanted string) bool {
	for _, directory := range directories {
		if directory == wanted {
			return true
		}
	}
	return false
}

func sortedStrings(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare([]byte(result[i]), []byte(result[j])) < 0 })
	return result
}
