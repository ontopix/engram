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
	"github.com/ontopix/engram/schemas"
)

// PlanSchemaCopy copies one exact embedded inventory entry into a new local
// schema path. It never creates a content directory; when needed it creates
// only the final scope-local .engram/schemas configuration chain.
func PlanSchemaCopy(ctx context.Context, root, typeName string, options SchemaCopyOptions) (*Plan, SchemaCopyResult, error) {
	const operation = "schema.copy"
	if !snapshot.ValidTypeSlug(typeName) {
		return nil, SchemaCopyResult{}, typed(ErrorUsage, operation, "", fmt.Errorf("invalid schema type %q", typeName))
	}
	if options.ScopeProvided && options.Scope == "" {
		return nil, SchemaCopyResult{}, typed(ErrorUsage, operation, "", errors.New("scope must not be empty when explicitly supplied"))
	}
	scope := normalizeRoot(options.Scope)
	if !validLogicalDirectory(scope) {
		return nil, SchemaCopyResult{}, typed(ErrorUsage, operation, scope, errors.New("scope must be a logical content directory"))
	}
	entry, err := inventoryEntry(typeName)
	if err != nil {
		return nil, SchemaCopyResult{}, err
	}

	p, err := openPlanner(ctx, operation, root)
	if err != nil {
		return nil, SchemaCopyResult{}, err
	}
	defer p.close()
	if !containsDirectory(p.checked.Tree.Directories, scope) {
		return nil, SchemaCopyResult{}, typed(ErrorRepository, operation, scope, errors.New("schema scope is not an existing real content directory"))
	}
	if _, err := p.captureDirectory(scope); err != nil {
		return nil, SchemaCopyResult{}, err
	}
	if err := p.captureSchemaTopology(scope); err != nil {
		return nil, SchemaCopyResult{}, err
	}
	if problem := invalidSchemaRoute(p.checked.Tree, scope); problem != "" {
		return nil, SchemaCopyResult{}, typed(ErrorRepository, operation, problem, errors.New("schema resolution route is not inspectable"))
	}

	configDirectory := joinLogical(scope, ".engram")
	configKind, configExists := p.checked.Tree.Boundaries[configDirectory]
	createConfigDirectory := false
	switch {
	case !configExists:
		if _, err := p.captureAbsent(configDirectory); err != nil {
			return nil, SchemaCopyResult{}, err
		}
		createConfigDirectory = true
	case configKind != snapshot.KindDirectory:
		return nil, SchemaCopyResult{}, typed(ErrorRepository, operation, configDirectory, errors.New("scope .engram boundary is not a real directory"))
	default:
		if _, err := p.captureDirectory(configDirectory); err != nil {
			return nil, SchemaCopyResult{}, err
		}
	}

	schemaDirectory := joinLogical(configDirectory, "schemas")
	schemaKind, schemaExists := p.checked.Tree.Boundaries[schemaDirectory]
	createSchemaDirectory := false
	switch {
	case !schemaExists:
		if _, err := p.captureAbsent(schemaDirectory); err != nil {
			return nil, SchemaCopyResult{}, err
		}
		createSchemaDirectory = true
	case schemaKind != snapshot.KindDirectory:
		return nil, SchemaCopyResult{}, typed(ErrorRepository, operation, schemaDirectory, errors.New("schema boundary is not a real directory"))
	default:
		if _, err := p.captureDirectory(schemaDirectory); err != nil {
			return nil, SchemaCopyResult{}, err
		}
	}

	destination := joinLogical(schemaDirectory, typeName+".md")
	absent, err := p.captureAbsent(destination)
	if err != nil {
		return nil, SchemaCopyResult{}, err
	}
	if conflict := schemaNameConflict(p.checked.Tree, scope, typeName, destination); conflict != "" {
		return nil, SchemaCopyResult{}, typed(ErrorConflict, operation, conflict, errors.New("copy would create forbidden schema shadowing"))
	}

	directories := []string(nil)
	if createConfigDirectory {
		directories = append(directories, configDirectory)
	}
	if createSchemaDirectory {
		directories = append(directories, schemaDirectory)
	}
	candidate, err := checker.CheckSource(newTreeOverlay(p.checked.Tree, map[string][]byte{
		destination: []byte(entry.Content),
	}, directories))
	if err != nil {
		return nil, SchemaCopyResult{}, typed(ErrorInternal, operation, destination, fmt.Errorf("validate copied schema candidate: %w", err))
	}
	baseErrors := errorIdentities(p.checked.Validation)
	for _, finding := range candidate.Validation.Findings {
		if len(finding.Code) == 0 || finding.Code[0] != 'E' {
			continue
		}
		if _, existed := baseErrors[[2]string{finding.Code, finding.Path}]; existed {
			continue
		}
		if finding.Path == destination || finding.Code == "E304" {
			return nil, SchemaCopyResult{}, typed(ErrorConflict, operation, finding.Path, fmt.Errorf("copied schema candidate has %s", finding.Code))
		}
	}

	if createConfigDirectory {
		p.addDirectory(configDirectory, 0o755)
	}
	if createSchemaDirectory {
		p.addDirectory(schemaDirectory, 0o755)
	}
	staging := schemaDirectory
	if createConfigDirectory {
		staging = scope
	} else if createSchemaDirectory {
		staging = configDirectory
	}
	p.addFile(destination, absent, []byte(entry.Content), staging)
	result := SchemaCopyResult{
		DryRun: options.DryRun, Changed: true,
		Schema: inventoryDescription(entry.Type, entry.Description, entry.Version), Path: destination,
	}
	return p.plan, result, nil
}

func errorIdentities(result checker.Result) map[[2]string]struct{} {
	identities := make(map[[2]string]struct{})
	for _, finding := range result.Findings {
		if len(finding.Code) != 0 && finding.Code[0] == 'E' {
			identities[[2]string{finding.Code, finding.Path}] = struct{}{}
		}
	}
	return identities
}

// SchemaCopy plans and, unless DryRun is set, publishes one inventory schema.
func SchemaCopy(ctx context.Context, root, typeName string, options SchemaCopyOptions) (SchemaCopyResult, error) {
	plan, result, err := PlanSchemaCopy(ctx, root, typeName, options)
	if err != nil {
		return SchemaCopyResult{}, err
	}
	if options.DryRun {
		return result, nil
	}
	if err := plan.PublishWith(ctx, options.Rendezvous); err != nil {
		return SchemaCopyResult{}, err
	}
	return result, nil
}

func inventoryEntry(typeName string) (schemas.Entry, error) {
	entries, err := schemas.Inventory()
	if err != nil {
		return schemas.Entry{}, typed(ErrorInternal, "schema.copy", "", fmt.Errorf("load embedded schema inventory: %w", err))
	}
	for _, entry := range entries {
		if entry.Type == typeName {
			return entry, nil
		}
	}
	return schemas.Entry{}, typed(ErrorUsage, "schema.copy", "", fmt.Errorf("schema type %q is not in the bundled inventory", typeName))
}

func schemaNameConflict(tree *snapshot.Tree, scope, typeName, destination string) string {
	filename := typeName + ".md"
	for ancestor := scope; ; ancestor = parentLogical(ancestor) {
		candidate := joinLogical(joinLogical(ancestor, ".engram/schemas"), filename)
		if candidate != destination {
			if _, exists := tree.Boundaries[candidate]; exists {
				return candidate
			}
		}
		if ancestor == "." {
			break
		}
	}
	candidates := make([]string, 0)
	for candidate := range tree.Boundaries {
		if path.Base(candidate) != filename || candidate == destination {
			continue
		}
		candidateScope := path.Dir(path.Dir(path.Dir(candidate)))
		if scope == "." || candidateScope != scope && len(candidateScope) > len(scope) && candidateScope[:len(scope)] == scope && candidateScope[len(scope)] == '/' {
			candidates = append(candidates, candidate)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return bytes.Compare([]byte(candidates[i]), []byte(candidates[j])) < 0 })
	if len(candidates) != 0 {
		return candidates[0]
	}
	return ""
}

func invalidSchemaRoute(tree *snapshot.Tree, scope string) string {
	for ancestor := scope; ; ancestor = parentLogical(ancestor) {
		config := joinLogical(ancestor, ".engram")
		if kind, exists := tree.Boundaries[config]; exists {
			if kind != snapshot.KindDirectory {
				return config
			}
			directory := joinLogical(config, "schemas")
			if kind, exists := tree.Boundaries[directory]; exists && kind != snapshot.KindDirectory {
				return directory
			}
		}
		if ancestor == "." {
			break
		}
	}
	return ""
}
