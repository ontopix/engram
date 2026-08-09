package documentprofile

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/markdownprofile"
	"github.com/ontopix/engram/internal/snapshot"
)

const (
	CatalogOpenMarker  = "<!-- engram:catalog -->"
	CatalogCloseMarker = "<!-- /engram:catalog -->"
)

var ErrCatalogMarkerShape = errors.New("catalog markers do not form exactly one ordered region")

// CatalogDirectory is one directly contained content directory.
type CatalogDirectory struct {
	Name        string
	Description string
}

// CatalogRecord is one directly contained record. Name is the complete
// logical filename, including its final .md suffix.
type CatalogRecord struct {
	Name        string
	Description string
	Pinned      bool
}

// EscapeCatalogText applies the single-pass §5.2 catalog-text escape without
// normalization or rescanning replacement text.
func EscapeCatalogText(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	for _, character := range value {
		switch character {
		case '&':
			result.WriteString("&amp;")
		case '<':
			result.WriteString("&lt;")
		case '>':
			result.WriteString("&gt;")
		case '\\':
			result.WriteString(`\\`)
		case '`':
			result.WriteString("\\`")
		case '*':
			result.WriteString("\\*")
		case '_':
			result.WriteString("\\_")
		case '[':
			result.WriteString("\\[")
		case ']':
			result.WriteString("\\]")
		default:
			result.WriteRune(character)
		}
	}
	return result.String()
}

// GenerateCatalog returns the exact machine-owned region for one directory.
// CatalogNone returns nil because that mode admits no markers.
func GenerateCatalog(mode CatalogMode, directories []CatalogDirectory, records []CatalogRecord) ([]byte, error) {
	if !ValidCatalogMode(mode) {
		return nil, fmt.Errorf("invalid catalog mode %q", mode)
	}
	if mode == CatalogNone {
		return nil, nil
	}
	if mode == CatalogDirs {
		// Record metadata is not an input to dirs-mode regeneration.
		records = nil
	}

	directories = append([]CatalogDirectory(nil), directories...)
	records = append([]CatalogRecord(nil), records...)
	if err := validateCatalogInputs(directories, records); err != nil {
		return nil, err
	}
	sort.Slice(directories, func(i, j int) bool {
		return bytes.Compare([]byte(directories[i].Name), []byte(directories[j].Name)) < 0
	})
	sort.Slice(records, func(i, j int) bool {
		left := strings.TrimSuffix(records[i].Name, ".md")
		right := strings.TrimSuffix(records[j].Name, ".md")
		return bytes.Compare([]byte(left), []byte(right)) < 0
	})

	var result strings.Builder
	result.WriteString(CatalogOpenMarker)
	result.WriteByte('\n')
	for _, directory := range directories {
		fmt.Fprintf(&result, "- [%s/](%s/README.md) — %s\n",
			EscapeCatalogText(directory.Name), directory.Name, EscapeCatalogText(directory.Description))
	}
	if mode != CatalogDirs {
		for _, record := range records {
			name := strings.TrimSuffix(record.Name, ".md")
			fmt.Fprintf(&result, "- [%s](%s.md)", EscapeCatalogText(name), name)
			if record.Pinned {
				result.WriteString(" (pinned)")
			}
			fmt.Fprintf(&result, " — %s\n", EscapeCatalogText(record.Description))
		}
	}
	result.WriteString(CatalogCloseMarker)
	result.WriteByte('\n')
	return []byte(result.String()), nil
}

func validateCatalogInputs(directories []CatalogDirectory, records []CatalogRecord) error {
	directoryNames := make(map[string]struct{}, len(directories))
	for _, directory := range directories {
		if !snapshot.ValidContentName(directory.Name) {
			return fmt.Errorf("catalog directory name %q is invalid", directory.Name)
		}
		if _, exists := directoryNames[directory.Name]; exists {
			return fmt.Errorf("catalog directory name %q is duplicated", directory.Name)
		}
		directoryNames[directory.Name] = struct{}{}
		if err := ValidateDescription(directory.Description); err != nil {
			return fmt.Errorf("catalog directory %q description: %w", directory.Name, err)
		}
	}
	recordNames := make(map[string]struct{}, len(records))
	for _, record := range records {
		if record.Name == "README.md" || !strings.HasSuffix(record.Name, ".md") || !snapshot.ValidContentName(record.Name) {
			return fmt.Errorf("catalog record name %q is invalid", record.Name)
		}
		if _, exists := recordNames[record.Name]; exists {
			return fmt.Errorf("catalog record name %q is duplicated", record.Name)
		}
		recordNames[record.Name] = struct{}{}
		if err := ValidateDescription(record.Description); err != nil {
			return fmt.Errorf("catalog record %q description: %w", record.Name, err)
		}
	}
	return nil
}

// CatalogRegion describes one opening/closing marker pair. All spans are
// relative to the README body; Entries excludes both marker lines.
type CatalogRegion struct {
	Span    markdownprofile.Span
	Opening markdownprofile.Span
	Entries markdownprofile.Span
	Closing markdownprofile.Span
}

// CatalogDetection records every exact whole-line marker occurrence.
type CatalogDetection struct {
	Openings []markdownprofile.Span
	Closings []markdownprofile.Span
}

// DetectCatalog scans marker lines byte-exactly. Marker-like substrings,
// indented lines, and lines with trailing bytes are ignored.
func DetectCatalog(body []byte) CatalogDetection {
	var detection CatalogDetection
	for start := 0; start < len(body); {
		relativeLF := bytes.IndexByte(body[start:], '\n')
		if relativeLF < 0 {
			break
		}
		end := start + relativeLF + 1
		line := body[start:end]
		switch {
		case bytes.Equal(line, []byte(CatalogOpenMarker+"\n")):
			detection.Openings = append(detection.Openings, markdownprofile.Span{Start: start, End: end})
		case bytes.Equal(line, []byte(CatalogCloseMarker+"\n")):
			detection.Closings = append(detection.Closings, markdownprofile.Span{Start: start, End: end})
		}
		start = end
	}
	return detection
}

// HasMarkers reports whether either exact marker occurs.
func (d CatalogDetection) HasMarkers() bool {
	return len(d.Openings) != 0 || len(d.Closings) != 0
}

// Region returns the sole correctly ordered catalog region.
func (d CatalogDetection) Region() (CatalogRegion, bool) {
	if len(d.Openings) != 1 || len(d.Closings) != 1 || d.Openings[0].Start >= d.Closings[0].Start {
		return CatalogRegion{}, false
	}
	return CatalogRegion{
		Span:    markdownprofile.Span{Start: d.Openings[0].Start, End: d.Closings[0].End},
		Opening: d.Openings[0],
		Entries: markdownprofile.Span{Start: d.Openings[0].End, End: d.Closings[0].Start},
		Closing: d.Closings[0],
	}, true
}

// ValidForMode reports the marker-shape part of §5.2. Byte-exact generated
// content is checked separately with RegionMatches.
func (d CatalogDetection) ValidForMode(mode CatalogMode) bool {
	if mode == CatalogNone {
		return !d.HasMarkers()
	}
	if mode != CatalogAll && mode != CatalogDirs {
		return false
	}
	_, ok := d.Region()
	return ok
}

// RegionMatches compares the existing sole region with exact regenerated
// bytes.
func (d CatalogDetection) RegionMatches(body, generated []byte) bool {
	region, ok := d.Region()
	return ok && validSpan(region.Span, len(body)) && bytes.Equal(body[region.Span.Start:region.Span.End], generated)
}

// ReplaceCatalog replaces an existing, structurally valid region and leaves
// every unrelated body byte untouched. It deliberately does not repair marker
// grammar. changed reports whether returned bytes differ.
func ReplaceCatalog(body, generated []byte) (result []byte, changed bool, err error) {
	detection := DetectCatalog(body)
	region, ok := detection.Region()
	if !ok {
		return nil, false, ErrCatalogMarkerShape
	}
	if bytes.Equal(body[region.Span.Start:region.Span.End], generated) {
		return append([]byte(nil), body...), false, nil
	}
	result = make([]byte, 0, len(body)-(region.Span.End-region.Span.Start)+len(generated))
	result = append(result, body[:region.Span.Start]...)
	result = append(result, generated...)
	result = append(result, body[region.Span.End:]...)
	return result, true, nil
}
