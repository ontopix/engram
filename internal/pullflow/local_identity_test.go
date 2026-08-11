package pullflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestObservePathRejectsRenameAndReplacement(t *testing.T) {
	tests := []struct {
		name   string
		create func(string) error
	}{
		{
			name: "regular",
			create: func(name string) error {
				return os.WriteFile(name, []byte("same bytes\n"), 0o600)
			},
		},
		{
			name: "directory",
			create: func(name string) error {
				return os.Mkdir(name, 0o700)
			},
		},
		{
			name: "symlink",
			create: func(name string) error {
				return os.Symlink("same-target", name)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			name := filepath.Join(root, "entry")
			if err := test.create(name); err != nil {
				if test.name == "symlink" {
					t.Skipf("symlink unavailable: %v", err)
				}
				t.Fatal(err)
			}
			displaced := filepath.Join(root, "displaced")
			seamCalled := false
			observed, err := observePathWith(root, "entry", func(observedName string) {
				seamCalled = true
				if observedName != name {
					t.Fatalf("observed path = %q, want %q", observedName, name)
				}
				if renameErr := os.Rename(name, displaced); renameErr != nil {
					t.Fatal(renameErr)
				}
				if createErr := test.create(name); createErr != nil {
					t.Fatal(createErr)
				}
			})
			if !seamCalled {
				t.Fatal("post-read seam was not called")
			}
			if err == nil || observed != nil {
				t.Fatalf("observation after replacement = %#v, %v; want rejection", observed, err)
			}
		})
	}
}
