package unicode17

import "testing"

func TestNFC(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"cafe\u0301":               "caf\u00e9",
		"\u212b":                   "\u00c5",
		"\u1100\u1161\u11a8":       "\uac01",
		"\u0061\u0315\u0300\u05ae": "\u00e0\u05ae\u0315",
	}
	for input, want := range tests {
		if got := NFC(input); got != want {
			t.Errorf("NFC(%q) = %q, want %q", input, got, want)
		}
		if !IsNFC(want) {
			t.Errorf("IsNFC(%q) = false", want)
		}
	}
}

func TestFullDefaultCaseFold(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"STRASSE":     "strasse",
		"stra\u00dfe": "strasse",
		"\u0130":      "i\u0307",
		"I":           "i",
	}
	for input, want := range tests {
		if got := CaseFold(input); got != want {
			t.Errorf("CaseFold(%q) = %q, want %q", input, got, want)
		}
	}
	if got := CaseFoldKey("stra\u00dfe"); got != CaseFoldKey("STRASSE") {
		t.Fatalf("case-fold keys differ: %q != %q", got, CaseFoldKey("STRASSE"))
	}
}
