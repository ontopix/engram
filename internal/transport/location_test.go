package transport

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/testpath"
)

func TestValidateLocationClosedSurface(t *testing.T) {
	t.Parallel()
	valid := []string{
		"https://example.test/owner/store.git",
		"ssh://git@example.test/owner/store.git",
		"file:///var/tmp/store.git",
		"git@example.test:owner/store.git",
		"example.test:owner/store.git",
		"example.test:/absolute/store.git",
		"example.test:-option-looking-remote-path",
	}
	for _, location := range valid {
		if err := ValidateLocation(location); err != nil {
			t.Errorf("ValidateLocation(%q) = %v", location, err)
		}
	}
	if location := testpath.FileURL(t.TempDir()); ValidateLocation(location) != nil {
		t.Errorf("ValidateLocation(host file URL %q) failed", location)
	}
	invalid := []string{
		"", "-uhoh", "http://example.test/store", "git://example.test/store",
		"ext::command", "ftp://example.test/store", "https://example.test",
		"https://example.test/store?x=1", "file://relative", "/local/path",
		"host:path\nnext", "@host:path",
	}
	for _, location := range invalid {
		if err := ValidateLocation(location); err == nil {
			t.Errorf("ValidateLocation(%q) succeeded", location)
		}
	}
}

func TestDataRootByPlatform(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	environment := map[string]string{"LOCALAPPDATA": filepath.Join(base, "local"), "XDG_DATA_HOME": filepath.Join(base, "xdg")}
	getenv := func(name string) string { return environment[name] }
	home := func() (string, error) { return filepath.Join(base, "home", "ada"), nil }
	tests := []struct {
		goos string
		want string
	}{
		{"darwin", filepath.Join(base, "home", "ada", "Library", "Application Support")},
		{"windows", filepath.Join(base, "local")},
		{"linux", filepath.Join(base, "xdg")},
	}
	for _, test := range tests {
		got, err := dataRoot(test.goos, getenv, home)
		if err != nil || got != test.want {
			t.Errorf("dataRoot(%q) = %q, %v; want %q", test.goos, got, err, test.want)
		}
	}
}

func TestDefaultDestinationUsesExactArgumentDigest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", t.TempDir())
	} else {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
	}
	location := "https://example.test/Owner/Store.git"
	destination, err := DefaultDestination(location)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := fmt.Sprintf("%x", sha256.Sum256([]byte(location)))
	if filepath.Base(destination) != wantSuffix || !strings.HasSuffix(filepath.Dir(destination), filepath.Join("engram", "stores")) {
		t.Fatalf("destination = %q", destination)
	}
}
