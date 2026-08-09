// Command gentables generates the Unicode 17 tables used by engram.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const unicodeVersion = "17.0.0"

var sourceHashes = map[string]string{
	"UnicodeData.txt":               "2e1efc1dcb59c575eedf5ccae60f95229f706ee6d031835247d843c11d96470c",
	"DerivedNormalizationProps.txt": "71fd6a206a2c0cdd41feb6b7f656aa31091db45e9cedc926985d718397f9e488",
	"CompositionExclusions.txt":     "2f239196ef3b5b61db5cc476e9bd80f534d15aa1b74e1be1dea5d042a344c85f",
	"CaseFolding.txt":               "ff8d8fefbf123574205085d6714c36149eb946d717a0c585c27f0f4ef58c4183",
	"NormalizationTest.txt":         "5019ffd530751a741900c849c0e010332f142a3612234639bd200b82138a87db",
}

type runeMapping struct {
	r      rune
	values []rune
}

type combiningClass struct {
	r     rune
	class uint8
}

type composition struct {
	left, right, composite rune
}

func main() {
	var ucdDir string
	var output string
	flag.StringVar(&ucdDir, "ucd-dir", "", "directory containing the pinned Unicode 17 source files")
	flag.StringVar(&output, "output", "internal/unicode17/tables_generated.go", "generated Go file")
	flag.Parse()

	if ucdDir == "" {
		fatal(errors.New("-ucd-dir is required"))
	}
	if err := verifySources(ucdDir); err != nil {
		fatal(err)
	}

	decompositions, classes, err := readUnicodeData(filepath.Join(ucdDir, "UnicodeData.txt"))
	if err != nil {
		fatal(err)
	}
	exclusions, err := readCompositionExclusions(
		filepath.Join(ucdDir, "CompositionExclusions.txt"),
		filepath.Join(ucdDir, "DerivedNormalizationProps.txt"),
	)
	if err != nil {
		fatal(err)
	}
	caseFolds, err := readCaseFolding(filepath.Join(ucdDir, "CaseFolding.txt"))
	if err != nil {
		fatal(err)
	}

	compositions := make([]composition, 0, len(decompositions))
	for _, mapping := range decompositions {
		if len(mapping.values) != 2 || exclusions[mapping.r] {
			continue
		}
		compositions = append(compositions, composition{
			left:      mapping.values[0],
			right:     mapping.values[1],
			composite: mapping.r,
		})
	}
	sort.Slice(compositions, func(i, j int) bool {
		return compositionKey(compositions[i].left, compositions[i].right) <
			compositionKey(compositions[j].left, compositions[j].right)
	})

	source, err := render(decompositions, classes, compositions, caseFolds)
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(output, source, 0o644); err != nil {
		fatal(err)
	}
}

func verifySources(dir string) error {
	for name, want := range sourceHashes {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if got != want {
			return fmt.Errorf("%s SHA-256 = %s, want %s", name, got, want)
		}
	}
	return nil
}

func readUnicodeData(path string) ([]runeMapping, []combiningClass, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	var mappings []runeMapping
	var classes []combiningClass
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		fields := strings.Split(scanner.Text(), ";")
		if len(fields) != 15 {
			return nil, nil, fmt.Errorf("%s:%d: got %d fields", path, lineNumber, len(fields))
		}
		codePoint, err := strconv.ParseUint(fields[0], 16, 32)
		if err != nil || codePoint > 0x10FFFF {
			return nil, nil, fmt.Errorf("%s:%d: invalid code point %q", path, lineNumber, fields[0])
		}
		if codePoint >= 0xD800 && codePoint <= 0xDFFF {
			continue
		}
		r := rune(codePoint)
		classValue, err := strconv.ParseUint(fields[3], 10, 8)
		if err != nil {
			return nil, nil, fmt.Errorf("%s:%d: combining class: %w", path, lineNumber, err)
		}
		if classValue != 0 {
			classes = append(classes, combiningClass{r: r, class: uint8(classValue)})
		}
		decomposition := fields[5]
		if decomposition == "" || strings.HasPrefix(decomposition, "<") {
			continue
		}
		values, err := parseRunes(decomposition)
		if err != nil {
			return nil, nil, fmt.Errorf("%s:%d: decomposition: %w", path, lineNumber, err)
		}
		mappings = append(mappings, runeMapping{r: r, values: values})
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	sort.Slice(mappings, func(i, j int) bool { return mappings[i].r < mappings[j].r })
	sort.Slice(classes, func(i, j int) bool { return classes[i].r < classes[j].r })
	return mappings, classes, nil
}

func readCompositionExclusions(paths ...string) (map[rune]bool, error) {
	exclusions := make(map[rune]bool)
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
			if line == "" {
				continue
			}
			parts := strings.Split(line, ";")
			if len(paths) > 1 && strings.HasSuffix(path, "DerivedNormalizationProps.txt") {
				if len(parts) < 2 || strings.TrimSpace(parts[1]) != "Full_Composition_Exclusion" {
					continue
				}
			}
			rangeText := strings.TrimSpace(parts[0])
			first, last, err := parseRuneRange(rangeText)
			if err != nil {
				file.Close()
				return nil, fmt.Errorf("%s:%d: %w", path, lineNumber, err)
			}
			for r := first; r <= last; r++ {
				exclusions[r] = true
			}
		}
		if err := scanner.Err(); err != nil {
			file.Close()
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	}
	return exclusions, nil
}

func readCaseFolding(path string) ([]runeMapping, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var mappings []runeMapping
	seen := make(map[rune]bool)
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		parts := strings.Split(line, ";")
		if len(parts) < 3 {
			return nil, fmt.Errorf("%s:%d: malformed case-fold row", path, lineNumber)
		}
		status := strings.TrimSpace(parts[1])
		if status != "C" && status != "F" {
			continue
		}
		r, err := parseRune(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNumber, err)
		}
		if seen[r] {
			return nil, fmt.Errorf("%s:%d: duplicate full/default mapping for U+%04X", path, lineNumber, r)
		}
		values, err := parseRunes(strings.TrimSpace(parts[2]))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNumber, err)
		}
		seen[r] = true
		mappings = append(mappings, runeMapping{r: r, values: values})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(mappings, func(i, j int) bool { return mappings[i].r < mappings[j].r })
	return mappings, nil
}

func render(
	decompositions []runeMapping,
	classes []combiningClass,
	compositions []composition,
	caseFolds []runeMapping,
) ([]byte, error) {
	var out bytes.Buffer
	fmt.Fprintf(&out, "// Code generated by internal/unicode17/cmd/gentables from Unicode %s; DO NOT EDIT.\n", unicodeVersion)
	fmt.Fprintln(&out, "package unicode17")
	fmt.Fprintln(&out)
	renderMappings(&out, "canonicalDecompositions", "canonicalDecompositionRunes", decompositions)
	fmt.Fprintln(&out, "var canonicalCombiningClasses = []classEntry{")
	for _, entry := range classes {
		fmt.Fprintf(&out, "{Rune: 0x%X, Class: %d},\n", entry.r, entry.class)
	}
	fmt.Fprintln(&out, "}")
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "var canonicalCompositions = []compositionEntry{")
	for _, entry := range compositions {
		fmt.Fprintf(&out, "{Key: 0x%016X, Composite: 0x%X},\n", compositionKey(entry.left, entry.right), entry.composite)
	}
	fmt.Fprintln(&out, "}")
	fmt.Fprintln(&out)
	renderMappings(&out, "caseFoldMappings", "caseFoldRunes", caseFolds)

	formatted, err := format.Source(out.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated source: %w", err)
	}
	return formatted, nil
}

func renderMappings(out *bytes.Buffer, entriesName, runesName string, mappings []runeMapping) {
	fmt.Fprintf(out, "var %s = []mappingEntry{\n", entriesName)
	offset := 0
	for _, mapping := range mappings {
		fmt.Fprintf(out, "{Rune: 0x%X, Offset: %d, Length: %d},\n", mapping.r, offset, len(mapping.values))
		offset += len(mapping.values)
	}
	fmt.Fprintln(out, "}")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "var %s = []rune{\n", runesName)
	for _, mapping := range mappings {
		for _, r := range mapping.values {
			fmt.Fprintf(out, "0x%X,\n", r)
		}
	}
	fmt.Fprintln(out, "}")
	fmt.Fprintln(out)
}

func parseRunes(value string) ([]rune, error) {
	parts := strings.Fields(value)
	result := make([]rune, 0, len(parts))
	for _, part := range parts {
		r, err := parseRune(part)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	if len(result) == 0 {
		return nil, errors.New("empty rune mapping")
	}
	return result, nil
}

func parseRune(value string) (rune, error) {
	n, err := strconv.ParseUint(value, 16, 32)
	if err != nil || n > 0x10FFFF || n >= 0xD800 && n <= 0xDFFF {
		return 0, fmt.Errorf("invalid Unicode scalar %q", value)
	}
	return rune(n), nil
}

func parseRuneRange(value string) (rune, rune, error) {
	parts := strings.Split(value, "..")
	if len(parts) > 2 {
		return 0, 0, fmt.Errorf("invalid rune range %q", value)
	}
	first, err := parseRune(parts[0])
	if err != nil {
		return 0, 0, err
	}
	last := first
	if len(parts) == 2 {
		last, err = parseRune(parts[1])
		if err != nil {
			return 0, 0, err
		}
	}
	if last < first {
		return 0, 0, fmt.Errorf("descending rune range %q", value)
	}
	return first, last, nil
}

func compositionKey(left, right rune) uint64 {
	return uint64(uint32(left))<<32 | uint64(uint32(right))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gentables:", err)
	os.Exit(1)
}
