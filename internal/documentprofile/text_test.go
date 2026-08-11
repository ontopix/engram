package documentprofile

import (
	"errors"
	"testing"
)

func TestValidateText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		source  []byte
		problem TextProblem
		offset  int
	}{
		{name: "invalid UTF-8", source: []byte{'a', 0xff, '\n'}, problem: TextInvalidUTF8, offset: 1},
		{name: "truncated UTF-8", source: []byte{'a', 0xe2, 0x82, '\n'}, problem: TextInvalidUTF8, offset: 1},
		{name: "BOM", source: []byte{0xef, 0xbb, 0xbf, 'a', '\n'}, problem: TextBOM, offset: 0},
		{name: "CRLF", source: []byte("a\r\n"), problem: TextCarriage, offset: 1},
		{name: "bare CR", source: []byte("a\rb\n"), problem: TextCarriage, offset: 1},
		{name: "missing LF", source: []byte("a"), problem: TextFinalLF, offset: 1},
		{name: "empty", source: nil, problem: TextFinalLF, offset: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateText(test.source)
			var textErr *TextError
			if !errors.As(err, &textErr) {
				t.Fatalf("error = %v, want *TextError", err)
			}
			if textErr.Problem != test.problem || textErr.Offset != test.offset {
				t.Fatalf("error = %#v, want problem %s at %d", textErr, test.problem, test.offset)
			}
			if IsNormedText(test.source) {
				t.Fatal("IsNormedText unexpectedly returned true")
			}
		})
	}
}

func TestValidateTextAcceptsNormedUnicodeAndInteriorFEFF(t *testing.T) {
	t.Parallel()
	source := []byte("café\ninside \ufeff is a character\n")
	if err := ValidateText(source); err != nil {
		t.Fatal(err)
	}
	if !IsNormedText(source) {
		t.Fatal("IsNormedText returned false")
	}
}
