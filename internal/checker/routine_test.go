package checker

import (
	"testing"

	"github.com/ontopix/engram/internal/snapshot"
)

func TestValidateRoutineDeclaration(t *testing.T) {
	t.Parallel()
	valid := []string{
		"17 2 * * *",
		"000 02 * 001 000",
		"*/15 0-23/2 * 1,2,3 1-5",
		"0 0 1 * *",
		"0 0 * * 0",
	}
	for _, cron := range valid {
		t.Run("valid_"+cron, func(t *testing.T) {
			t.Parallel()
			if err := validateRoutineDeclaration(routineDeclaration(cron, "# Instructions\n")); err != nil {
				t.Fatalf("validateRoutineDeclaration(%q): %v", cron, err)
			}
		})
	}

	invalid := []struct {
		name string
		data []byte
	}{
		{"missing frontmatter", []byte("# Instructions\n")},
		{"missing format", []byte("---\ncron: \"0 0 * * *\"\n---\n# Instructions\n")},
		{"wrong format type", []byte("---\nengram: 1\ncron: \"0 0 * * *\"\n---\n# Instructions\n")},
		{"unknown field", []byte("---\nengram: routine/v1\ncron: \"0 0 * * *\"\ntimezone: UTC\n---\n# Instructions\n")},
		{"wrong cron type", []byte("---\nengram: routine/v1\ncron: 1\n---\n# Instructions\n")},
		{"empty instructions", routineDeclaration("0 0 * * *", " \t\n")},
		{"double field space", routineDeclaration("0  0 * * *", "# Instructions\n")},
		{"both day fields constrained", routineDeclaration("0 0 1 * 1", "# Instructions\n")},
		{"number step", routineDeclaration("0 0 1/2 * *", "# Instructions\n")},
		{"zero step", routineDeclaration("*/0 0 * * *", "# Instructions\n")},
		{"step exceeds range", routineDeclaration("0-5/7 0 * * *", "# Instructions\n")},
		{"alias", routineDeclaration("0 0 * * SUN", "# Instructions\n")},
		{"out of range", routineDeclaration("60 0 * * *", "# Instructions\n")},
		{"bad range", routineDeclaration("0 0 31-1 * *", "# Instructions\n")},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := validateRoutineDeclaration(test.data); err == nil {
				t.Fatal("invalid routine declaration accepted")
			}
		})
	}
}

func TestRoutineRoleUsesE309(t *testing.T) {
	t.Parallel()
	const routinePath = "journal/.engram/routines/daily-journal.md"
	analysis := &snapshotAnalysis{
		tree: &snapshot.Tree{Files: map[string]snapshot.File{
			routinePath: {
				Path: routinePath,
				Role: snapshot.RoleRoutine,
				Data: routineDeclaration("0 0 * * *", " \n"),
			},
		}},
		findings:  make(findingSet),
		validText: map[string]bool{routinePath: true},
	}
	analysis.parseDocuments()
	findings := analysis.findings.sorted()
	if len(findings) != 1 || findings[0].Code != "E309" || findings[0].Path != routinePath {
		t.Fatalf("findings = %#v, want E309 at %q", findings, routinePath)
	}
}

func routineDeclaration(cron, body string) []byte {
	return []byte("---\nengram: routine/v1\ncron: \"" + cron + "\"\n---\n" + body)
}
