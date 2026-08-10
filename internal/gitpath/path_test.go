package gitpath

import (
	"path/filepath"
	"testing"
)

func TestAbsoluteTranslatesGitSeparatorsToNativePath(t *testing.T) {
	native, err := filepath.Abs(filepath.Join("one", "two"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Absolute(filepath.ToSlash(native))
	if err != nil || got != native {
		t.Fatalf("Absolute = %q, %v; want %q", got, err, native)
	}
}

func TestAbsoluteRejectsLexicalNormalization(t *testing.T) {
	root, err := filepath.Abs(string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	value := filepath.ToSlash(filepath.Join(root, "one")) + "/../two"
	if _, err := Absolute(value); err == nil {
		t.Fatal("non-canonical Git path was accepted")
	}
}
