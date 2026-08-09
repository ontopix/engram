// Package unicode17 implements the Unicode 17.0.0 operations required by
// engram path identity. The generated tables are independent of the Unicode
// version bundled with the Go toolchain.
package unicode17

import (
	"sort"
	"strings"
)

// Version is the Unicode data version used by this package.
const Version = "17.0.0"

type mappingEntry struct {
	Rune   rune
	Offset uint32
	Length uint8
}

type classEntry struct {
	Rune  rune
	Class uint8
}

type compositionEntry struct {
	Key       uint64
	Composite rune
}

const (
	hangulSBase  = rune(0xAC00)
	hangulLBase  = rune(0x1100)
	hangulVBase  = rune(0x1161)
	hangulTBase  = rune(0x11A7)
	hangulLCount = rune(19)
	hangulVCount = rune(21)
	hangulTCount = rune(28)
	hangulNCount = hangulVCount * hangulTCount
	hangulSCount = hangulLCount * hangulNCount
)

// NFC returns the canonical NFC normalization of s. Callers must reject
// invalid UTF-8 before calling it; Go's rune iteration would otherwise replace
// invalid bytes with U+FFFD.
func NFC(s string) string {
	if s == "" {
		return ""
	}

	decomposed := make([]rune, 0, len(s))
	for _, r := range s {
		decompose(r, &decomposed)
	}
	canonicalOrder(decomposed)
	composed := compose(decomposed)
	return string(composed)
}

// IsNFC reports whether s is already in Unicode 17.0.0 NFC.
func IsNFC(s string) bool {
	return NFC(s) == s
}

// CaseFold applies Unicode 17.0.0 Full Default Case Folding. Turkic mappings
// are intentionally excluded.
func CaseFold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		entry, ok := lookupMapping(caseFoldMappings, r)
		if !ok {
			b.WriteRune(r)
			continue
		}
		for _, folded := range caseFoldRunes[entry.Offset : entry.Offset+uint32(entry.Length)] {
			b.WriteRune(folded)
		}
	}
	return b.String()
}

// CaseFoldKey implements the exact engram collision key:
// NFC(toCasefold(NFC(name))).
func CaseFoldKey(s string) string {
	return NFC(CaseFold(NFC(s)))
}

func decompose(r rune, dst *[]rune) {
	if r >= hangulSBase && r < hangulSBase+hangulSCount {
		sIndex := r - hangulSBase
		l := hangulLBase + sIndex/hangulNCount
		v := hangulVBase + (sIndex%hangulNCount)/hangulTCount
		t := hangulTBase + sIndex%hangulTCount
		*dst = append(*dst, l, v)
		if t != hangulTBase {
			*dst = append(*dst, t)
		}
		return
	}

	entry, ok := lookupMapping(canonicalDecompositions, r)
	if !ok {
		*dst = append(*dst, r)
		return
	}
	for _, child := range canonicalDecompositionRunes[entry.Offset : entry.Offset+uint32(entry.Length)] {
		decompose(child, dst)
	}
}

func canonicalOrder(runes []rune) {
	for i := 1; i < len(runes); i++ {
		class := combiningClass(runes[i])
		if class == 0 {
			continue
		}
		for j := i; j > 0; j-- {
			previous := combiningClass(runes[j-1])
			if previous == 0 || previous <= class {
				break
			}
			runes[j-1], runes[j] = runes[j], runes[j-1]
		}
	}
}

func compose(runes []rune) []rune {
	if len(runes) < 2 {
		return runes
	}

	result := make([]rune, 0, len(runes))
	result = append(result, runes[0])
	starterPosition := 0
	starter := runes[0]
	lastClass := combiningClass(starter)

	for _, current := range runes[1:] {
		class := combiningClass(current)
		composite, ok := composePair(starter, current)
		if ok && (lastClass == 0 || lastClass < class) {
			result[starterPosition] = composite
			starter = composite
			continue
		}

		if class == 0 {
			starterPosition = len(result)
			starter = current
		}
		result = append(result, current)
		lastClass = class
	}

	return result
}

func composePair(left, right rune) (rune, bool) {
	if left >= hangulLBase && left < hangulLBase+hangulLCount &&
		right >= hangulVBase && right < hangulVBase+hangulVCount {
		lIndex := left - hangulLBase
		vIndex := right - hangulVBase
		return hangulSBase + (lIndex*hangulVCount+vIndex)*hangulTCount, true
	}
	if left >= hangulSBase && left < hangulSBase+hangulSCount &&
		(left-hangulSBase)%hangulTCount == 0 &&
		right > hangulTBase && right < hangulTBase+hangulTCount {
		return left + (right - hangulTBase), true
	}

	key := compositionKey(left, right)
	i := sort.Search(len(canonicalCompositions), func(i int) bool {
		return canonicalCompositions[i].Key >= key
	})
	if i < len(canonicalCompositions) && canonicalCompositions[i].Key == key {
		return canonicalCompositions[i].Composite, true
	}
	return 0, false
}

func combiningClass(r rune) uint8 {
	i := sort.Search(len(canonicalCombiningClasses), func(i int) bool {
		return canonicalCombiningClasses[i].Rune >= r
	})
	if i < len(canonicalCombiningClasses) && canonicalCombiningClasses[i].Rune == r {
		return canonicalCombiningClasses[i].Class
	}
	return 0
}

func lookupMapping(entries []mappingEntry, r rune) (mappingEntry, bool) {
	i := sort.Search(len(entries), func(i int) bool { return entries[i].Rune >= r })
	if i < len(entries) && entries[i].Rune == r {
		return entries[i], true
	}
	return mappingEntry{}, false
}

func compositionKey(left, right rune) uint64 {
	return uint64(uint32(left))<<32 | uint64(uint32(right))
}
