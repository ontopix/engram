package pullflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/replay"
)

var presentationAttributes = []string{"text", "eol", "filter", "ident", "working-tree-encoding"}

type lineageAudit struct {
	Tip        string
	Validation checker.Result
	Audits     []managedread.HistoryAudit
	Raw        *gitraw.Audit
	Snapshots  map[string]*checker.Snapshot
}

func auditIncoming(ctx context.Context, repository *gitraw.Repository, tip gitraw.OID) (*lineageAudit, error) {
	raw, err := gitraw.AuditLineage(ctx, repository, tip)
	if err != nil {
		return nil, err
	}
	result := &lineageAudit{
		Tip:        tip.String(),
		Validation: checker.Result{Target: checker.TargetManagedStore, Status: checker.StatusComplete},
		Audits:     []managedread.HistoryAudit{}, Raw: raw,
		Snapshots: make(map[string]*checker.Snapshot, len(raw.Commits)),
	}
	findings := make(map[[2]string]checker.Finding)
	for _, finding := range raw.Findings {
		addFinding(findings, checker.Finding{Code: finding.Code, Path: finding.Path, Detail: strings.Join(finding.Detail, "; ")})
	}
	visited := make(map[string]struct{}, len(raw.Commits))
	for _, audited := range raw.Commits {
		id := audited.ID.String()
		visited[id] = struct{}{}
		if audited.Commit == nil || audited.Snapshot == nil {
			continue
		}
		snapshot, checkErr := checker.CheckSource(gitraw.NewTreeSource(ctx, repository, audited.Commit.Tree))
		if checkErr != nil {
			return nil, checkErr
		}
		result.Snapshots[id] = snapshot
		for _, finding := range snapshot.Validation.Findings {
			addFinding(findings, finding)
		}
	}
	for _, audited := range raw.Commits {
		id := audited.ID.String()
		candidate := result.Snapshots[id]
		if candidate == nil || audited.Commit == nil || len(audited.Commit.Parents) > 1 {
			continue
		}
		var base *checker.Snapshot
		var baseID *string
		initialization := len(audited.Commit.Parents) == 0
		if !initialization {
			parent := audited.Commit.Parents[0].String()
			if _, ok := visited[parent]; !ok {
				continue
			}
			base = result.Snapshots[parent]
			if base == nil {
				continue
			}
			baseID = stringPointer(parent)
		}
		validation, _ := checker.CheckTransition(base, candidate, initialization)
		if validation.Status == checker.StatusIndeterminate {
			result.Validation.Status = checker.StatusIndeterminate
		}
		for _, finding := range validation.Findings {
			addFinding(findings, finding)
		}
		result.Audits = append(result.Audits, managedread.HistoryAudit{Base: baseID, Candidate: id, Validation: validation})
	}
	result.Validation.Findings = orderedFindings(findings)
	return result, nil
}

func requireCompleteIncoming(audit *lineageAudit) error {
	if audit == nil || audit.Raw == nil || !audit.Raw.Complete {
		return errors.New("incoming raw lineage audit is incomplete")
	}
	if len(audit.Raw.Commits) == 0 || len(audit.Audits) != len(audit.Raw.Commits) || len(audit.Snapshots) != len(audit.Raw.Commits) {
		return errors.New("incoming snapshot or transition audit is incomplete")
	}
	if audit.Raw.Tip.String() != audit.Tip || audit.Raw.Commits[len(audit.Raw.Commits)-1].ID.String() != audit.Tip {
		return errors.New("incoming audit tip is inconsistent")
	}
	for index, commit := range audit.Raw.Commits {
		if commit.Commit == nil || commit.Snapshot == nil || audit.Audits[index].Candidate != commit.ID.String() || audit.Audits[index].Validation.Status != checker.StatusComplete {
			return errors.New("incoming commit lacks a complete matching transition audit")
		}
	}
	return nil
}

func addFinding(set map[[2]string]checker.Finding, finding checker.Finding) {
	key := [2]string{finding.Code, finding.Path}
	if previous, ok := set[key]; ok {
		if previous.Detail == "" && finding.Detail != "" {
			set[key] = finding
		}
		return
	}
	set[key] = finding
}

func orderedFindings(set map[[2]string]checker.Finding) []checker.Finding {
	result := make([]checker.Finding, 0, len(set))
	for _, finding := range set {
		result = append(result, finding)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path != result[j].Path {
			return bytes.Compare([]byte(result[i].Path), []byte(result[j].Path)) < 0
		}
		return result[i].Code < result[j].Code
	})
	return result
}

func historyIDs(raw *gitraw.Audit) []string {
	if raw == nil {
		return nil
	}
	result := make([]string, len(raw.Commits))
	for index, commit := range raw.Commits {
		result[index] = commit.ID.String()
	}
	return result
}

func commonPrefix(local *managedread.AcceptedAudit, remote *lineageAudit) int {
	localIDs := historyIDs(local.Raw)
	remoteIDs := historyIDs(remote.Raw)
	count := 0
	for count < len(localIDs) && count < len(remoteIDs) && localIDs[count] == remoteIDs[count] {
		count++
	}
	return count
}

func snapshotFiles(snapshot *checker.Snapshot) replay.Files {
	result := make(replay.Files)
	if snapshot == nil || snapshot.Tree == nil {
		return result
	}
	for name, file := range snapshot.Tree.Files {
		result[name] = append([]byte(nil), file.Data...)
	}
	return result
}

func sourceRecords(local *managedread.AcceptedAudit, common int) ([]sourceCommit, error) {
	if local == nil || local.Raw == nil || common < 1 || common > len(local.Raw.Commits) {
		return nil, errors.New("invalid divergent local lineage")
	}
	result := make([]sourceCommit, 0, len(local.Raw.Commits)-common)
	for _, audited := range local.Raw.Commits[common:] {
		if audited.Commit == nil || len(audited.Commit.Parents) != 1 {
			return nil, errors.New("local replay source is not a single-parent commit")
		}
		id := audited.ID.String()
		result = append(result, sourceCommit{ID: id, Base: audited.Commit.Parents[0].String(), Message: "Replay " + id})
	}
	return result, nil
}

func snapshotAt(ctx context.Context, repository *gitraw.Repository, id string) (*checker.Snapshot, error) {
	oid, err := gitraw.ParseOID(repository.Format, id)
	if err != nil {
		return nil, err
	}
	source, _, err := repository.SnapshotSource(ctx, oid)
	if err != nil {
		return nil, err
	}
	return checker.CheckSource(source)
}

func (p *Puller) auditIncomingAttributes(ctx context.Context, git string, repository *gitraw.Repository, audit *lineageAudit) error {
	if audit == nil || audit.Snapshots[audit.Tip] == nil || audit.Snapshots[audit.Tip].Tree == nil {
		return errors.New("incoming tip snapshot is unavailable")
	}
	paths := make([]string, 0, len(audit.Snapshots[audit.Tip].Tree.Files))
	for name := range audit.Snapshots[audit.Tip].Tree.Files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil
	}
	var input bytes.Buffer
	for _, name := range paths {
		input.WriteString(name)
		input.WriteByte(0)
	}
	arguments := append([]string{"check-attr", "-z", "--stdin"}, presentationAttributes...)
	result := p.command(ctx, git, repository.Root, input.Bytes(), arguments...)
	if !result.started {
		return typed(ErrorCapability, "inspect incoming presentation attributes", result.err)
	}
	if ctx.Err() != nil {
		return typed(ErrorCancelled, "inspect incoming presentation attributes", ctx.Err())
	}
	if result.err != nil || result.status != 0 {
		return typed(ErrorRepository, "inspect incoming presentation attributes", errors.New(commandDetail(result)))
	}
	issues, err := validateIncomingAttributes(paths, result.stdout)
	if err != nil {
		return typed(ErrorRepository, "inspect incoming presentation attributes", err)
	}
	if len(issues) != 0 {
		findings := make(map[[2]string]checker.Finding, len(audit.Validation.Findings)+1)
		for _, finding := range audit.Validation.Findings {
			addFinding(findings, finding)
		}
		addFinding(findings, checker.Finding{Code: "E601", Path: ".", Detail: strings.Join(issues, "; ")})
		audit.Validation.Findings = orderedFindings(findings)
	}
	return nil
}

func validateIncomingAttributes(paths []string, output []byte) ([]string, error) {
	fields := bytes.Split(output, []byte{0})
	if len(fields) != 0 && len(fields[len(fields)-1]) == 0 {
		fields = fields[:len(fields)-1]
	}
	if len(fields) != len(paths)*len(presentationAttributes)*3 {
		return nil, errors.New("Git returned an incomplete attribute result")
	}
	allowed := make(map[string]struct{}, len(presentationAttributes))
	for _, name := range presentationAttributes {
		allowed[name] = struct{}{}
	}
	seen := make(map[string]map[string]bool, len(paths))
	issues := make([]string, 0)
	for offset := 0; offset < len(fields); offset += 3 {
		if !utf8.Valid(fields[offset]) || !utf8.Valid(fields[offset+1]) || !utf8.Valid(fields[offset+2]) {
			return nil, errors.New("Git returned non-UTF-8 attribute data")
		}
		path, attribute, value := string(fields[offset]), string(fields[offset+1]), string(fields[offset+2])
		index := sort.SearchStrings(paths, path)
		if index == len(paths) || paths[index] != path {
			return nil, errors.New("Git returned an unexpected attribute path")
		}
		if _, ok := allowed[attribute]; !ok {
			return nil, errors.New("Git returned an unexpected attribute name")
		}
		if seen[path] == nil {
			seen[path] = make(map[string]bool)
		}
		if seen[path][attribute] {
			return nil, errors.New("Git returned a duplicate attribute record")
		}
		seen[path][attribute] = true
		admitted := value == "unspecified" || attribute == "text" && value == "unset"
		if !admitted {
			issues = append(issues, fmt.Sprintf("attribute %s=%s transforms %s", attribute, value, path))
		}
	}
	sort.Strings(issues)
	return issues, nil
}

func modesAt(ctx context.Context, repository *gitraw.Repository, id string) (map[string]gitraw.TreeMode, error) {
	oid, err := gitraw.ParseOID(repository.Format, id)
	if err != nil {
		return nil, err
	}
	object, err := repository.ReadObject(ctx, oid)
	if err != nil {
		return nil, err
	}
	if object.Type != gitraw.TypeCommit {
		return nil, fmt.Errorf("%s is not a commit", id)
	}
	commit, err := gitraw.ParseCommit(repository.Format, object.Data)
	if err != nil {
		return nil, err
	}
	result := make(map[string]gitraw.TreeMode)
	if err := walkModes(ctx, repository, commit.Tree, "", result); err != nil {
		return nil, err
	}
	return result, nil
}

func walkModes(ctx context.Context, repository *gitraw.Repository, treeID gitraw.OID, prefix string, result map[string]gitraw.TreeMode) error {
	object, err := repository.ReadObject(ctx, treeID)
	if err != nil {
		return err
	}
	if object.Type != gitraw.TypeTree {
		return fmt.Errorf("tree target has type %s", object.Type)
	}
	entries, err := gitraw.ParseTree(repository.Format, object.Data)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := string(entry.Name)
		if prefix != "" {
			name = prefix + "/" + name
		}
		switch {
		case entry.Mode.IsDirectory():
			if err := walkModes(ctx, repository, entry.OID, name, result); err != nil {
				return err
			}
		case entry.Mode.IsRegular():
			result[name] = entry.Mode
		}
	}
	return nil
}
