package releasepack

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDeterministicArchivesContainExactRootedAssets(t *testing.T) {
	assets := []asset{
		{name: "engram", data: []byte("binary\x00bytes"), mode: 0o755},
		{name: "docs/operator-guide.md", data: []byte("guide\n"), mode: 0o644},
	}
	for _, test := range []struct {
		name  string
		write func(string, string, []asset, int64) error
		read  func(*testing.T, string) map[string]archiveEntry
	}{
		{name: "tar-gzip", write: writeTarGzip, read: readTarGzip},
		{name: "zip", write: writeZIP, read: readZIP},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := filepath.Join(t.TempDir(), "first")
			second := filepath.Join(t.TempDir(), "second")
			if err := test.write(first, "engram-v1.0.0-test", assets, 1_700_000_000); err != nil {
				t.Fatal(err)
			}
			if err := test.write(second, "engram-v1.0.0-test", assets, 1_700_000_000); err != nil {
				t.Fatal(err)
			}
			firstBytes, _ := os.ReadFile(first)
			secondBytes, _ := os.ReadFile(second)
			if !bytes.Equal(firstBytes, secondBytes) {
				t.Fatal("same inputs produced different archive bytes")
			}
			entries := test.read(t, first)
			want := map[string]archiveEntry{
				"engram-v1.0.0-test/engram":                 {data: []byte("binary\x00bytes"), mode: 0o755},
				"engram-v1.0.0-test/docs/operator-guide.md": {data: []byte("guide\n"), mode: 0o644},
			}
			if !reflect.DeepEqual(entries, want) {
				t.Fatalf("entries = %#v", entries)
			}
		})
	}
}

func TestVersionRevisionAndTargetValidation(t *testing.T) {
	for _, valid := range []string{"1.0.0", "v1.2.3", "v1.0.0-rc.1"} {
		version, label, err := normalizeVersion(valid)
		if err != nil || version == "" || label[0] != 'v' {
			t.Errorf("normalize %q = %q, %q, %v", valid, version, label, err)
		}
	}
	for _, invalid := range []string{"", "v1", "v1.2", "v01.2.3", "v1.2.3-", "v1.2.3+meta", "v1.2.x", "v1.2.3-.", "v1.2.3-rc..1", "v1.2.3-rc.01"} {
		if _, _, err := normalizeVersion(invalid); err == nil {
			t.Errorf("invalid version %q accepted", invalid)
		}
	}
	if !validRevision("0123456789012345678901234567890123456789") || validRevision("ABC") {
		t.Fatal("revision validation differs")
	}
	if _, err := normalizeTargets([]Target{{OS: "linux", Arch: "amd64"}, {OS: "linux", Arch: "amd64"}}); err == nil {
		t.Fatal("duplicate target accepted")
	}
	for _, target := range []Target{{OS: "../linux", Arch: "amd64"}, {OS: "linux/x", Arch: "amd64"}, {OS: "linux", Arch: "AMD64"}} {
		if _, err := normalizeTargets([]Target{target}); err == nil {
			t.Errorf("unsafe target %#v accepted", target)
		}
	}
}

func TestBuildBindsRequestedVersionToExactSourceVersion(t *testing.T) {
	_, err := Build(t.Context(), filepath.Join("..", ".."), Options{
		Version: "v9.9.9", Revision: strings.Repeat("0", 40),
	})
	if err == nil || !strings.Contains(err.Error(), "differs from the exact source CLI version") {
		t.Fatalf("mismatched source version error = %v", err)
	}
}

func TestPublicationIsAtomicAndNeverOverwrites(t *testing.T) {
	directory := t.TempDir()
	staged := filepath.Join(directory, "staged")
	destination := filepath.Join(directory, "artifact")
	if err := os.WriteFile(staged, []byte("complete bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := publishFile(staged, destination); err != nil {
		t.Fatal(err)
	}
	stagedInfo, _ := os.Lstat(staged)
	destinationInfo, _ := os.Lstat(destination)
	if !os.SameFile(stagedInfo, destinationInfo) {
		t.Fatal("publication did not use one atomic linked image")
	}
	if err := publishFile(staged, destination); err == nil {
		t.Fatal("publication overwrote an existing artifact")
	}
}

func TestReleaseAssetsBindSkillsAndUnicodeProvenance(t *testing.T) {
	root := filepath.Join("..", "..")
	assets, err := releaseAssets(context.Background(), root, testSourceFiles(t, root), DefaultTargets)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"skills/manifest-v1.json":                        false,
		"licenses/go/LICENSE.txt":                        false,
		"licenses/go/PATENTS.txt":                        false,
		"licenses/third-party/golang-x-text-PATENTS.txt": false,
		"licenses/third-party/yaml-v3-NOTICE.txt":        false,
		"provenance/unicode17/LICENSE.txt":               false,
		"provenance/unicode17/README.md":                 false,
		"docs/spec/README.md":                            false,
		"schemas/note.md":                                false,
		"examples/minimal/.engram/root.yaml":             false,
	}
	for _, current := range assets {
		if _, exists := want[current.name]; exists {
			want[current.name] = true
		}
	}
	for name, present := range want {
		if !present {
			t.Errorf("release asset %s is missing", name)
		}
	}
}

func TestExactSourceMaterializationIgnoresWorktreeAndIgnoredFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("system Git is unavailable")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/release\n\ngo 1.25.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("committed bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runReleaseGit(t, root, "init", "--quiet")
	runReleaseGit(t, root, "config", "user.name", "Release Test")
	runReleaseGit(t, root, "config", "user.email", "release@example.test")
	runReleaseGit(t, root, "add", "go.mod", "tracked.txt")
	runReleaseGit(t, root, "commit", "--quiet", "--no-gpg-sign", "-m", "release fixture")
	revision := strings.TrimSpace(runReleaseGit(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("racing worktree bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("ignored.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.md"), []byte("must not enter release\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	source, tracked, err := materializeReleaseSource(t.Context(), root, revision)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(source) })
	data, err := os.ReadFile(filepath.Join(source, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "committed bytes\n" {
		t.Fatalf("materialized tracked bytes = %q", data)
	}
	if _, exists := tracked["ignored.md"]; exists {
		t.Fatal("ignored worktree file entered exact tracked set")
	}
	if _, err := os.Stat(filepath.Join(source, "ignored.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ignored worktree file was materialized: %v", err)
	}
}

func TestReleaseSourceMustBeExactCleanHEAD(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("system Git is unavailable")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/ontopix/engram\n\ngo 1.25.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked"), []byte("release source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runReleaseGit(t, root, "init", "--quiet")
	runReleaseGit(t, root, "config", "user.name", "Release Test")
	runReleaseGit(t, root, "config", "user.email", "release@example.test")
	runReleaseGit(t, root, "add", "go.mod", "tracked")
	runReleaseGit(t, root, "commit", "--quiet", "--no-gpg-sign", "-m", "release fixture")
	revision := strings.TrimSpace(runReleaseGit(t, root, "rev-parse", "HEAD"))
	if err := verifyReleaseSource(context.Background(), root, revision); err != nil {
		t.Fatalf("clean exact source rejected: %v", err)
	}
	wrong := strings.Repeat("0", len(revision))
	if err := verifyReleaseSource(context.Background(), root, wrong); err == nil {
		t.Fatal("wrong release revision accepted")
	}
	runReleaseGit(t, root, "config", "core.worktree", t.TempDir())
	if err := verifyReleaseSource(context.Background(), root, revision); err == nil {
		t.Fatal("redirected release worktree accepted")
	}
	runReleaseGit(t, root, "config", "--unset", "core.worktree")
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("ignored.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.go"), []byte("package ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseSource(context.Background(), root, revision); err == nil {
		t.Fatal("ignored release input accepted")
	}
	if err := os.Remove(filepath.Join(root, "ignored.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked"), []byte("not released\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseSource(context.Background(), root, revision); err == nil {
		t.Fatal("dirty release source accepted")
	}
	if err := os.Remove(filepath.Join(root, "untracked")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked"), []byte("changed release source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseSource(context.Background(), root, revision); err == nil {
		t.Fatal("modified tracked release input accepted")
	}
}

func TestReleaseSourceVerificationDoesNotExecuteCleanFilters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the filter sentinel uses a POSIX program")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("system Git is unavailable")
	}
	root := t.TempDir()
	tracked := filepath.Join(root, "tracked")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/ontopix/engram\n\ngo 1.25.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("tracked filter=poison\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracked, []byte("release source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runReleaseGit(t, root, "init", "--quiet")
	runReleaseGit(t, root, "config", "user.name", "Release Test")
	runReleaseGit(t, root, "config", "user.email", "release@example.test")
	runReleaseGit(t, root, "add", "go.mod", ".gitattributes", "tracked")
	runReleaseGit(t, root, "commit", "--quiet", "--no-gpg-sign", "-m", "release fixture")
	revision := strings.TrimSpace(runReleaseGit(t, root, "rev-parse", "HEAD"))

	sentinel := filepath.Join(root, ".git", "clean-filter-ran")
	program := filepath.Join(root, ".git", "poison-filter")
	programBytes := []byte(fmt.Sprintf("#!/bin/sh\nprintf invoked > %q\ncat\n", sentinel))
	if err := os.WriteFile(program, programBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	runReleaseGit(t, root, "config", "filter.poison.clean", program)
	runReleaseGit(t, root, "config", "filter.poison.required", "true")
	if err := os.Chtimes(tracked, time.Now().Add(2*time.Hour), time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	runReleaseGit(t, root, "status", "--porcelain=v1")
	if _, err := os.Lstat(sentinel); err != nil {
		t.Fatalf("sentinel setup did not demonstrate Git clean-filter execution: %v", err)
	}
	if err := os.Remove(sentinel); err != nil {
		t.Fatal(err)
	}

	if err := verifyReleaseSource(t.Context(), root, revision); err != nil {
		t.Fatalf("pure release source verification rejected clean bytes: %v", err)
	}
	if _, err := os.Lstat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("release source verification executed repository clean filter: %v", err)
	}
}

func TestBuildEnvironmentIgnoresHostileGoOverrides(t *testing.T) {
	t.Setenv("GOAMD64", "v3")
	t.Setenv("GOARM64", "v9.5")
	t.Setenv("GOENV", "/hostile/goenv")
	t.Setenv("GOEXPERIMENT", "fieldtrack")
	t.Setenv("GOWORK", "/hostile/go.work")
	t.Setenv("GOPROXY", "https://example.invalid")
	environment := buildEnvironment(Target{OS: "linux", Arch: "amd64"})
	joined := "\n" + strings.Join(environment, "\n") + "\n"
	for _, want := range []string{"\nGOOS=linux\n", "\nGOARCH=amd64\n", "\nGOAMD64=v1\n", "\nGOENV=off\n", "\nGOWORK=off\n", "\nGOPROXY=off\n", "\nGOTOOLCHAIN=local\n"} {
		if !strings.Contains(joined, want) {
			t.Errorf("isolated build environment lacks %q: %q", want, environment)
		}
	}
	for _, forbidden := range []string{"GOAMD64=v3", "GOARM64=v9.5", "fieldtrack", "example.invalid", "/hostile"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("isolated build environment retained %q", forbidden)
		}
	}
}

func TestReleaseGitEnvironmentHasOneAuthoritativeLocale(t *testing.T) {
	environment := isolatedReleaseGitEnvironment([]string{
		"PATH=/bin",
		"LC_ALL=tr_TR.UTF-8",
		"GIT_CONFIG_GLOBAL=/hostile/config",
		"ENGRAM_STATE=/hostile/state",
	})
	joined := "\n" + strings.Join(environment, "\n") + "\n"
	if strings.Count(joined, "\nLC_ALL=") != 1 || !strings.Contains(joined, "\nLC_ALL=C\n") {
		t.Fatalf("isolated Git locale = %q", environment)
	}
	for _, forbidden := range []string{"tr_TR", "/hostile/config", "/hostile/state"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("isolated Git environment retained %q", forbidden)
		}
	}
}

func TestReleaseGitDisablesWorktreeObservers(t *testing.T) {
	want := []string{
		"-c", "core.longpaths=true",
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false",
		"-c", "gc.auto=0",
		"-C", "/exact/repository",
	}
	if got := releaseGitPrefix("/exact/repository"); !reflect.DeepEqual(got, want) {
		t.Fatalf("release Git prefix = %#v, want %#v", got, want)
	}
}

func TestReleaseToolchainMustMatchProvenanceBuilder(t *testing.T) {
	if err := requireMatchingToolchain("go1.25.12", "go1.25.12"); err != nil {
		t.Fatal(err)
	}
	if err := requireMatchingToolchain("go1.26.3", "go1.25.12"); err == nil {
		t.Fatal("different release compiler and provenance builder accepted")
	}
}

func runReleaseGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = isolatedReleaseGitEnvironment(os.Environ())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(output)
}

func testSourceFiles(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	result := make(map[string]struct{})
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = struct{}{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type archiveEntry struct {
	data []byte
	mode os.FileMode
}

func readTarGzip(t *testing.T, name string) map[string]archiveEntry {
	t.Helper()
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	result := make(map[string]archiveEntry)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		result[header.Name] = archiveEntry{data: data, mode: os.FileMode(header.Mode)}
		if !header.ModTime.Equal(time.Unix(1_700_000_000, 0)) {
			t.Fatalf("timestamp = %s", header.ModTime)
		}
	}
	return result
}

func readZIP(t *testing.T, name string) map[string]archiveEntry {
	t.Helper()
	reader, err := zip.OpenReader(name)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	result := make(map[string]archiveEntry)
	for _, entry := range reader.File {
		file, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			t.Fatal(readErr, closeErr)
		}
		result[entry.Name] = archiveEntry{data: data, mode: entry.Mode().Perm()}
	}
	return result
}
