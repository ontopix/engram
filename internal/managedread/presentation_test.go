package managedread

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/gitraw"
)

func TestAuditPresentationConfigAndFingerprint(t *testing.T) {
	root := newGitRepository(t, gitraw.SHA1, true)
	store := openStore(t, root)
	accepted, err := store.Accepted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	findings, first, err := store.auditPresentation(context.Background(), store.repository, accepted.Snapshot)
	if err != nil || len(findings) != 0 {
		t.Fatalf("clean presentation = %#v, %v", findings, err)
	}
	_, repeated, err := store.auditPresentation(context.Background(), store.repository, accepted.Snapshot)
	if err != nil || !first.Equal(repeated) || first.String() == "" {
		t.Fatalf("fingerprint repeat = %s / %s, %v", first, repeated, err)
	}

	runGit(t, root, nil, "config", "core.autocrlf", "true")
	findings, changed, err := store.auditPresentation(context.Background(), store.repository, accepted.Snapshot)
	if err != nil || !hasFinding(findings, "E601", ".") {
		t.Fatalf("autocrlf presentation = %#v, %v", findings, err)
	}
	if first.Equal(changed) {
		t.Fatal("presentation fingerprint did not change with config")
	}
}

func TestAuditPresentationRejectsNonRootSelection(t *testing.T) {
	root := newGitRepository(t, gitraw.SHA1, true)
	store := openStore(t, root)
	accepted, err := store.Accepted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	repository := *store.repository
	repository.Root = filepath.Join(root, "topics")
	findings, _, err := store.auditPresentation(context.Background(), &repository, accepted.Snapshot)
	if err != nil || !hasFinding(findings, "E601", ".") || !strings.Contains(findings[0].Detail, "worktree root") {
		t.Fatalf("non-root presentation = %#v, %v", findings, err)
	}
}

func TestAuditPresentationAttributes(t *testing.T) {
	root := newGitRepository(t, gitraw.SHA1, true)
	writeFile(t, filepath.Join(root, ".gitattributes"), []byte("*.md text eol=crlf filter=unsafe ident working-tree-encoding=UTF-16\n"), 0o644)
	store := openStore(t, root)
	accepted, err := store.Accepted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	findings, _, err := store.auditPresentation(context.Background(), store.repository, accepted.Snapshot)
	if err != nil || !hasFinding(findings, "E601", ".") {
		t.Fatalf("attribute presentation = %#v, %v", findings, err)
	}
	detail := findings[0].Detail
	for _, attribute := range presentationAttributes {
		if !strings.Contains(detail, "attribute "+attribute+"=") {
			t.Fatalf("detail %q does not identify %s", detail, attribute)
		}
	}
}

func TestAuditPresentationSparseAndSkipWorktree(t *testing.T) {
	t.Run("config", func(t *testing.T) {
		root := newGitRepository(t, gitraw.SHA1, true)
		runGit(t, root, nil, "config", "core.sparseCheckout", "true")
		store := openStore(t, root)
		accepted, err := store.Accepted(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		findings, _, err := store.auditPresentation(context.Background(), store.repository, accepted.Snapshot)
		if err != nil || !hasFinding(findings, "E601", ".") || !strings.Contains(findings[0].Detail, "sparse") {
			t.Fatalf("sparse presentation = %#v, %v", findings, err)
		}
	})

	t.Run("skip-worktree", func(t *testing.T) {
		root := newGitRepository(t, gitraw.SHA1, true)
		runGit(t, root, nil, "update-index", "--skip-worktree", "README.md")
		store := openStore(t, root)
		accepted, err := store.Accepted(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		findings, _, err := store.auditPresentation(context.Background(), store.repository, accepted.Snapshot)
		if err != nil || !hasFinding(findings, "E601", ".") || !strings.Contains(findings[0].Detail, "skip-worktree") {
			t.Fatalf("skip-worktree presentation = %#v, %v", findings, err)
		}
	})
}

func TestAuditPresentationAllowsUnsetText(t *testing.T) {
	root := newGitRepository(t, gitraw.SHA1, true)
	writeFile(t, filepath.Join(root, ".gitattributes"), []byte("*.md -text\n"), 0o644)
	store := openStore(t, root)
	accepted, err := store.Accepted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	findings, _, err := store.auditPresentation(context.Background(), store.repository, accepted.Snapshot)
	if err != nil || len(findings) != 0 {
		t.Fatalf("unset text presentation = %#v, %v", findings, err)
	}
}
