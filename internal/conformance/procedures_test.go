package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type procedureManifest struct {
	Version     int                   `json:"version"`
	Obligations []procedureObligation `json:"obligations"`
}

type procedureObligation struct {
	ID       string              `json:"id"`
	Area     string              `json:"area"`
	Evidence []procedureEvidence `json:"evidence"`
}

type procedureEvidence struct {
	File string `json:"file"`
	Test string `json:"test"`
}

func TestProceduralManifestIsClosedCompleteAndReferencesTests(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "testdata", "conformance", "procedures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest procedureManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		t.Fatalf("decode procedural manifest: %v", err)
	}
	if manifest.Version != 1 || manifest.Obligations == nil {
		t.Fatalf("manifest header = %#v", manifest)
	}
	wantIDs := []string{
		"attachment.alias-binding", "attachment.concurrent-update", "attachment.malformed-block", "attachment.roundtrip-preservation",
		"executor.base-immutability", "executor.candidate-capture", "executor.closed-protocol-order", "executor.resource-bounds", "executor.trust-before-execution",
		"synchronizer.clone-private-verification", "synchronizer.pull-conflict-abort", "synchronizer.pull-fast-forward-audit", "synchronizer.pull-recovery", "synchronizer.pull-replay-oldest-first", "synchronizer.push-conditional", "synchronizer.push-indeterminate-outcome", "synchronizer.push-network-boundary",
		"writer.concurrent-input", "writer.dry-run-no-mutation", "writer.final-candidate-hook-once", "writer.init-atomic-publication", "writer.preserve-unrelated-drafts", "writer.recovery-boundaries", "writer.ref-cas-ambiguity", "writer.revert-inverse", "writer.sha1-sha256",
	}
	gotIDs := make([]string, 0, len(manifest.Obligations))
	areas := map[string]int{"attachment": 0, "executor": 0, "synchronizer": 0, "writer": 0}
	seenEvidence := make(map[string]struct{})
	for index, obligation := range manifest.Obligations {
		gotIDs = append(gotIDs, obligation.ID)
		if index > 0 && manifest.Obligations[index-1].ID >= obligation.ID {
			t.Errorf("obligations are not strictly ordered at %q", obligation.ID)
		}
		if _, admitted := areas[obligation.Area]; !admitted || !strings.HasPrefix(obligation.ID, obligation.Area+".") {
			t.Errorf("obligation %q has invalid area %q", obligation.ID, obligation.Area)
		}
		areas[obligation.Area]++
		if obligation.Evidence == nil || len(obligation.Evidence) == 0 {
			t.Errorf("obligation %q has no evidence", obligation.ID)
		}
		for _, evidence := range obligation.Evidence {
			if filepath.IsAbs(evidence.File) || filepath.Clean(evidence.File) != filepath.FromSlash(evidence.File) || strings.Contains(evidence.File, "..") || !strings.HasSuffix(evidence.File, "_test.go") {
				t.Errorf("obligation %q has unsafe evidence path %q", obligation.ID, evidence.File)
				continue
			}
			key := evidence.File + "#" + evidence.Test
			if _, duplicate := seenEvidence[key]; duplicate {
				t.Errorf("evidence %q is referenced more than once", key)
			}
			seenEvidence[key] = struct{}{}
			source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(evidence.File)))
			if err != nil {
				t.Errorf("read evidence %q: %v", key, err)
				continue
			}
			declaration := []byte(fmt.Sprintf("func %s(", evidence.Test))
			if !bytes.Contains(source, declaration) {
				t.Errorf("evidence %q has no matching test declaration", key)
			}
		}
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("procedural obligation IDs:\n got: %q\nwant: %q", gotIDs, wantIDs)
	}
	for area, count := range areas {
		if count == 0 {
			t.Errorf("procedural area %q has no obligation", area)
		}
	}
	if !sort.StringsAreSorted(gotIDs) {
		t.Error("procedural obligation IDs are not sorted")
	}
}
