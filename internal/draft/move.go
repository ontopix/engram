package draft

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"sort"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/internal/unicode17"
)

// PlanMove computes one literal record move, all required link rewrites, and
// affected catalog regeneration without publishing any bytes.
func PlanMove(ctx context.Context, root, from, to string, options MoveOptions) (*Plan, MoveResult, error) {
	const operation = "mv"
	if !validRecordPath(from) {
		return nil, MoveResult{}, typed(ErrorUsage, operation, from, errors.New("source must be a valid store-relative .md record path other than README.md"))
	}
	if !validRecordPath(to) {
		return nil, MoveResult{}, typed(ErrorUsage, operation, to, errors.New("destination must be a valid store-relative .md record path other than README.md"))
	}
	if from == to {
		return nil, MoveResult{}, typed(ErrorUsage, operation, from, errors.New("source and destination must be distinct literal paths"))
	}

	p, err := openPlanner(ctx, operation, root)
	if err != nil {
		return nil, MoveResult{}, err
	}
	defer p.close()

	sourceFile, exists := p.checked.Tree.Files[from]
	if !exists || sourceFile.Role != snapshot.RoleRecord {
		return nil, MoveResult{}, typed(ErrorRepository, operation, from, errors.New("source is not an existing regular record"))
	}
	sourceRecord := p.checked.Records[from]
	if sourceRecord == nil || sourceRecord.Description == nil || !sourceRecord.PinnedValid {
		return nil, MoveResult{}, typed(ErrorRepository, operation, from, errors.New("source record metadata is unavailable"))
	}
	sourceParent := parentLogical(from)
	destinationParent := parentLogical(to)
	if !containsDirectory(p.checked.Tree.Directories, sourceParent) {
		return nil, MoveResult{}, typed(ErrorRepository, operation, sourceParent, errors.New("source parent is not an existing real content directory"))
	}
	if !containsDirectory(p.checked.Tree.Directories, destinationParent) {
		return nil, MoveResult{}, typed(ErrorRepository, operation, destinationParent, errors.New("destination parent is not an existing real content directory"))
	}

	// Every content-directory entry set and every Markdown-bearing file is an
	// input to the complete inbound-link search. Capturing them prevents a new
	// or replaced document from escaping the plan between scan and publish.
	for _, directory := range p.checked.Tree.Directories {
		if err := cancelled(ctx, operation); err != nil {
			return nil, MoveResult{}, err
		}
		if _, err := p.captureDirectory(directory); err != nil {
			return nil, MoveResult{}, err
		}
	}
	sourceObservation, err := p.captureFile(from, sourceFile.Data)
	if err != nil {
		return nil, MoveResult{}, err
	}
	absent, err := p.captureAbsent(to)
	if err != nil {
		return nil, MoveResult{}, err
	}
	for existing := range p.checked.Tree.Boundaries {
		if parentLogical(existing) == destinationParent && unicode17.CaseFoldKey(path.Base(existing)) == unicode17.CaseFoldKey(path.Base(to)) {
			return nil, MoveResult{}, typed(ErrorConflict, operation, to, fmt.Errorf("destination name collides case-insensitively with %q", existing))
		}
	}
	// Schema files are Markdown-bearing documents too. The complete topology
	// capture prevents a concurrently created schema from introducing an
	// unscanned inbound occurrence and also freezes destination resolution.
	if err := p.captureSchemaTopology("."); err != nil {
		return nil, MoveResult{}, err
	}

	addition := &catalogAddition{path: to, description: *sourceRecord.Description, pinned: sourceRecord.Pinned}
	catalogBytes := make(map[string][]byte)
	catalogSet := make(map[string]struct{})
	if sourceParent == destinationParent {
		catalogPath, updated, changed, catalogErr := p.planCatalogMutation(sourceParent, from, addition)
		if catalogErr != nil {
			return nil, MoveResult{}, catalogErr
		}
		if changed {
			catalogBytes[catalogPath] = updated
			catalogSet[catalogPath] = struct{}{}
		}
	} else {
		catalogPath, updated, changed, catalogErr := p.planCatalogMutation(sourceParent, from, nil)
		if catalogErr != nil {
			return nil, MoveResult{}, catalogErr
		}
		if changed {
			catalogBytes[catalogPath] = updated
			catalogSet[catalogPath] = struct{}{}
		}
		catalogPath, updated, changed, catalogErr = p.planCatalogMutation(destinationParent, "", addition)
		if catalogErr != nil {
			return nil, MoveResult{}, catalogErr
		}
		if changed {
			catalogBytes[catalogPath] = updated
			catalogSet[catalogPath] = struct{}{}
		}
	}

	documentNames := make([]string, 0)
	for name, file := range p.checked.Tree.Files {
		switch file.Role {
		case snapshot.RoleRecord, snapshot.RoleMap, snapshot.RoleSchema:
			documentNames = append(documentNames, name)
		}
	}
	sort.Slice(documentNames, func(i, j int) bool { return bytes.Compare([]byte(documentNames[i]), []byte(documentNames[j])) < 0 })
	finalFiles := make(map[string][]byte)
	linkPaths := make(map[string]struct{})
	for _, name := range documentNames {
		if err := cancelled(ctx, operation); err != nil {
			return nil, MoveResult{}, err
		}
		file := p.checked.Tree.Files[name]
		if _, err := p.captureFile(name, file.Data); err != nil {
			return nil, MoveResult{}, err
		}
		input := file.Data
		if generated, changed := catalogBytes[name]; changed {
			input = generated
		}
		finalName := name
		if name == from {
			finalName = to
		}
		updated, linksChanged, rewriteErr := rewriteMoveDocument(name, finalName, input, file.Role == snapshot.RoleRecord, from, to)
		if rewriteErr != nil {
			return nil, MoveResult{}, typed(ErrorConflict, operation, name, rewriteErr)
		}
		if linksChanged {
			linkPaths[finalName] = struct{}{}
		}
		if name == from || !bytes.Equal(updated, file.Data) {
			finalFiles[finalName] = updated
		}
	}

	candidate, err := checker.CheckSource(newTreeOverlayWithDeletes(p.checked.Tree, finalFiles, nil, []string{from}))
	if err != nil {
		return nil, MoveResult{}, typed(ErrorInternal, operation, to, fmt.Errorf("validate move candidate: %w", err))
	}
	if finding, found := firstNewError(p.checked.Validation, candidate.Validation); found {
		return nil, MoveResult{}, typed(ErrorConflict, operation, finding.Path, fmt.Errorf("move candidate has %s", finding.Code))
	}

	destinationBytes, exists := finalFiles[to]
	if !exists {
		return nil, MoveResult{}, typed(ErrorInternal, operation, to, errors.New("move plan has no destination image"))
	}
	p.addFileMode(to, absent, destinationBytes, destinationParent, sourceObservation.mode)
	otherNames := make([]string, 0, len(finalFiles))
	for name := range finalFiles {
		if name != to {
			otherNames = append(otherNames, name)
		}
	}
	sort.Slice(otherNames, func(i, j int) bool { return bytes.Compare([]byte(otherNames[i]), []byte(otherNames[j])) < 0 })
	for _, name := range otherNames {
		p.addFile(name, p.plan.captures[name], finalFiles[name], parentLogical(name))
	}
	// Deleting the source last makes every earlier failure rollback while the
	// original record still exists. A later failure restores its exact bytes.
	p.addDelete(from, sourceObservation)

	result := MoveResult{
		DryRun: options.DryRun, Changed: true, From: from, To: to,
		Paths: sortedStrings(linkPaths), Catalogs: sortedStrings(catalogSet),
	}
	return p.plan, result, nil
}

// Move plans and, unless DryRun is set, publishes one complete record move.
func Move(ctx context.Context, root, from, to string, options MoveOptions) (MoveResult, error) {
	plan, result, err := PlanMove(ctx, root, from, to, options)
	if err != nil {
		return MoveResult{}, err
	}
	if options.DryRun {
		return result, nil
	}
	if err := plan.PublishWith(ctx, options.Rendezvous); err != nil {
		return MoveResult{}, err
	}
	return result, nil
}
