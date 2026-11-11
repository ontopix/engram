// Package typeref defines the exact, filesystem-native schema reference grammar.
package typeref

import "strings"

// Reference keeps canonical decimal digits rather than a machine-sized integer.
type Reference struct{ Name, Version string }

func (r Reference) String() string { return r.Name + "@v" + r.Version }

func ValidName(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] == '-' || value[len(value)-1] == '-' || strings.Contains(value, "--") {
		return false
	}
	for _, c := range []byte(value) {
		if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func Parse(value string) (Reference, bool) {
	name, version, ok := strings.Cut(value, "@v")
	if !ok || !ValidName(name) || len(version) == 0 || version[0] < '1' || version[0] > '9' {
		return Reference{}, false
	}
	for _, c := range []byte(version) {
		if c < '0' || c > '9' {
			return Reference{}, false
		}
	}
	return Reference{Name: name, Version: version}, true
}

func Valid(value string) bool { _, ok := Parse(value); return ok }

func Filename(value string) (Reference, bool) {
	if !strings.HasSuffix(value, ".md") {
		return Reference{}, false
	}
	return Parse(strings.TrimSuffix(value, ".md"))
}

// CompareVersions compares canonical positive decimal integers without overflow.
func CompareVersions(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

// Matches admits either a logical name or one exact revision of that name.
func Matches(constraint, value string) bool {
	r, ok := Parse(value)
	return ok && (constraint == r.Name || constraint == value)
}
