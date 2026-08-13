package managedread

import (
	"reflect"
	"testing"

	"github.com/ontopix/engram/internal/gitraw"
)

func TestPrunedIndexPathsRetainsRoutineDeclarations(t *testing.T) {
	t.Parallel()
	entries := map[string]IndexEntry{
		".engram/routines/daily-journal.md": {
			Mode: gitraw.ModeRegular,
		},
		"journal/.engram/routines/weekly-review.md": {
			Mode: gitraw.ModeRegular,
		},
		".engram/unknown/state": {
			Mode: gitraw.ModeRegular,
		},
	}
	if got, want := prunedIndexPaths(entries), []string{".engram/unknown/state"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prunedIndexPaths = %v, want %v", got, want)
	}
}
