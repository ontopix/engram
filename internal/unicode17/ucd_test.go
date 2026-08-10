package unicode17

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestUnicodeNormalizationConformance runs the complete checked-in Unicode
// normalization corpus. An explicit directory remains available for auditing
// a separately acquired copy of the same pinned sources.
func TestUnicodeNormalizationConformance(t *testing.T) {
	dir := unicodeSourceDirectory()

	path := filepath.Join(dir, "NormalizationTest.txt")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNumber := 0
	rows := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line == "" || strings.HasPrefix(line, "@") {
			continue
		}
		fields := strings.Split(line, ";")
		if len(fields) < 5 {
			t.Fatalf("%s:%d: malformed row", path, lineNumber)
		}
		values := make([]string, 5)
		for i := range values {
			values[i], err = decodeCodePoints(strings.TrimSpace(fields[i]))
			if err != nil {
				t.Fatalf("%s:%d: field %d: %v", path, lineNumber, i+1, err)
			}
		}

		assertNFC(t, path, lineNumber, "c1", values[0], values[1])
		assertNFC(t, path, lineNumber, "c2", values[1], values[1])
		assertNFC(t, path, lineNumber, "c3", values[2], values[1])
		assertNFC(t, path, lineNumber, "c4", values[3], values[3])
		assertNFC(t, path, lineNumber, "c5", values[4], values[3])
		rows++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		t.Fatal("normalization corpus contained no rows")
	}
}

func TestUnicodeCaseFoldingConformance(t *testing.T) {
	dir := unicodeSourceDirectory()

	path := filepath.Join(dir, "CaseFolding.txt")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNumber := 0
	rows := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		fields := strings.Split(line, ";")
		if len(fields) < 3 {
			t.Fatalf("%s:%d: malformed row", path, lineNumber)
		}
		status := strings.TrimSpace(fields[1])
		if status != "C" && status != "F" {
			continue
		}
		input, err := decodeCodePoints(strings.TrimSpace(fields[0]))
		if err != nil {
			t.Fatalf("%s:%d: input: %v", path, lineNumber, err)
		}
		want, err := decodeCodePoints(strings.TrimSpace(fields[2]))
		if err != nil {
			t.Fatalf("%s:%d: mapping: %v", path, lineNumber, err)
		}
		if got := CaseFold(input); got != want {
			t.Fatalf("%s:%d: CaseFold(%U) = %U, want %U", path, lineNumber, []rune(input), []rune(got), []rune(want))
		}
		rows++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		t.Fatal("case-folding corpus contained no applicable rows")
	}
}

func TestPinnedUnicodeSourceHashes(t *testing.T) {
	want := map[string]string{
		"UnicodeData.txt":               "2e1efc1dcb59c575eedf5ccae60f95229f706ee6d031835247d843c11d96470c",
		"DerivedNormalizationProps.txt": "71fd6a206a2c0cdd41feb6b7f656aa31091db45e9cedc926985d718397f9e488",
		"CompositionExclusions.txt":     "2f239196ef3b5b61db5cc476e9bd80f534d15aa1b74e1be1dea5d042a344c85f",
		"CaseFolding.txt":               "ff8d8fefbf123574205085d6714c36149eb946d717a0c585c27f0f4ef58c4183",
		"NormalizationTest.txt":         "5019ffd530751a741900c849c0e010332f142a3612234639bd200b82138a87db",
	}
	for name, expected := range want {
		data, err := os.ReadFile(filepath.Join(unicodeSourceDirectory(), name))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if actual := hex.EncodeToString(digest[:]); actual != expected {
			t.Errorf("%s SHA-256 = %s, want %s", name, actual, expected)
		}
	}
}

func unicodeSourceDirectory() string {
	if directory := os.Getenv("ENGRAM_UNICODE17_UCD_DIR"); directory != "" {
		return directory
	}
	return filepath.Join("data", "ucd")
}

func assertNFC(t *testing.T, path string, line int, field, input, want string) {
	t.Helper()
	if got := NFC(input); got != want {
		t.Fatalf("%s:%d: NFC(%s) = %U, want %U", path, line, field, []rune(got), []rune(want))
	}
}

func decodeCodePoints(value string) (string, error) {
	runes := make([]rune, 0, len(strings.Fields(value)))
	for _, field := range strings.Fields(value) {
		codePoint, err := strconv.ParseUint(field, 16, 32)
		if err != nil || codePoint > 0x10FFFF || codePoint >= 0xD800 && codePoint <= 0xDFFF {
			return "", fmt.Errorf("invalid Unicode scalar %q", field)
		}
		runes = append(runes, rune(codePoint))
	}
	return string(runes), nil
}
