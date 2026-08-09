// Package documentprofile contains the source-level rules shared by engram
// records, directory maps, and schema documents.
package documentprofile

import (
	"bytes"
	"fmt"
	"unicode/utf8"
)

// TextProblem identifies one violation of the normed-text contract in core
// specification §2.6.
type TextProblem string

const (
	TextInvalidUTF8 TextProblem = "invalid-utf8"
	TextBOM         TextProblem = "byte-order-mark"
	TextCarriage    TextProblem = "carriage-return"
	TextFinalLF     TextProblem = "missing-final-lf"
)

// TextError attributes a normed-text failure to a zero-based byte offset.
type TextError struct {
	Problem TextProblem
	Offset  int
}

func (e *TextError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("normed text %s at byte %d", e.Problem, e.Offset)
}

// ValidateText enforces UTF-8, absence of a leading byte-order mark and any
// carriage return, and the mandatory final LF. It does not impose Markdown or
// YAML structure.
func ValidateText(source []byte) error {
	if !utf8.Valid(source) {
		return &TextError{Problem: TextInvalidUTF8, Offset: firstInvalidUTF8(source)}
	}
	if bytes.HasPrefix(source, []byte{0xef, 0xbb, 0xbf}) {
		return &TextError{Problem: TextBOM, Offset: 0}
	}
	if offset := bytes.IndexByte(source, '\r'); offset >= 0 {
		return &TextError{Problem: TextCarriage, Offset: offset}
	}
	if len(source) == 0 || source[len(source)-1] != '\n' {
		return &TextError{Problem: TextFinalLF, Offset: len(source)}
	}
	return nil
}

// IsNormedText reports whether source satisfies the complete text framing
// contract.
func IsNormedText(source []byte) bool {
	return ValidateText(source) == nil
}

func firstInvalidUTF8(source []byte) int {
	for offset := 0; offset < len(source); {
		_, width := utf8.DecodeRune(source[offset:])
		if width == 1 && source[offset] >= utf8.RuneSelf {
			return offset
		}
		offset += width
	}
	return len(source)
}
