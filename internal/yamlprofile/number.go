package yamlprofile

import (
	"encoding/json"
	"math/big"
	"strings"
)

// NumberResolution identifies the Core Schema rule that resolved a number.
// It does not affect the number's mathematical value: in particular, a value
// resolved by the float rule can still satisfy an integer constraint.
type NumberResolution uint8

const (
	IntegerResolution NumberResolution = iota + 1
	FloatResolution
)

// Number is an exact finite YAML Core Schema number. Its value is
// coefficient * 10^exponent. The pair is normalized: a non-zero coefficient
// is not divisible by ten and zero always has exponent zero.
//
// Keeping the exponent as an arbitrary-precision integer means that parsing
// never rounds, truncates, or overflows merely because a decimal exponent is
// outside a machine integer's range.
type Number struct {
	coefficient big.Int
	exponent    big.Int
	spelling    string
	resolution  NumberResolution
}

// Resolution reports whether YAML's Core Schema resolved the source spelling
// through its integer or finite-float rule.
func (n *Number) Resolution() NumberResolution {
	if n == nil {
		return 0
	}
	return n.resolution
}

// Source returns the original plain-scalar spelling.
func (n *Number) Source() string {
	if n == nil {
		return ""
	}
	return n.spelling
}

// Coefficient returns a copy of the exact normalized coefficient.
func (n *Number) Coefficient() *big.Int {
	if n == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(&n.coefficient)
}

// Exponent returns a copy of the exact normalized base-10 exponent.
func (n *Number) Exponent() *big.Int {
	if n == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(&n.exponent)
}

// Sign returns -1, 0, or +1 according to the exact mathematical value.
func (n *Number) Sign() int {
	if n == nil {
		return 0
	}
	return n.coefficient.Sign()
}

// IsInteger reports whether the exact mathematical value has no fractional
// part. This deliberately does not inspect the YAML source spelling.
func (n *Number) IsInteger() bool {
	return n != nil && (n.coefficient.Sign() == 0 || n.exponent.Sign() >= 0)
}

// IsPositiveInteger reports whether the value is both integral and greater
// than zero.
func (n *Number) IsPositiveInteger() bool {
	return n != nil && n.IsInteger() && n.Sign() > 0
}

// CanonicalJSON returns a compact JSON-number spelling for the exact value.
// It is suitable for json.Number and never uses a binary floating-point
// conversion.
func (n *Number) CanonicalJSON() string {
	if n == nil || n.coefficient.Sign() == 0 {
		return "0"
	}
	coefficient := n.coefficient.String()
	if n.exponent.Sign() == 0 {
		return coefficient
	}
	return coefficient + "e" + n.exponent.String()
}

// JSONNumber returns this value as a precision-preserving json.Number.
func (n *Number) JSONNumber() json.Number {
	return json.Number(n.CanonicalJSON())
}

var _ json.Marshaler = (*Number)(nil)

// MarshalJSON implements json.Marshaler without losing numeric precision.
func (n *Number) MarshalJSON() ([]byte, error) {
	return []byte(n.CanonicalJSON()), nil
}

func (n *Number) String() string {
	return n.CanonicalJSON()
}

// Cmp compares two exact values and returns -1, 0, or +1. It compares very
// large exponents without expanding them into correspondingly large big.Ints.
func (n *Number) Cmp(other *Number) int {
	if n == nil {
		if other == nil || other.Sign() == 0 {
			return 0
		}
		return -other.Sign()
	}
	if other == nil {
		return n.Sign()
	}
	leftSign, rightSign := n.Sign(), other.Sign()
	if leftSign != rightSign {
		if leftSign < rightSign {
			return -1
		}
		return 1
	}
	if leftSign == 0 {
		return 0
	}

	cmp := compareMagnitude(n, other)
	if leftSign < 0 {
		return -cmp
	}
	return cmp
}

// CmpInt64 compares the exact value with a machine integer without first
// converting the YAML value to that machine type.
func (n *Number) CmpInt64(value int64) int {
	other := numberFromInt64(value)
	return n.Cmp(other)
}

// IsIntegerInRange checks an inclusive int64 range by mathematical value.
// It is useful for bounded fields while still rejecting neither huge valid
// YAML numbers nor fractional spellings prematurely.
func (n *Number) IsIntegerInRange(minimum, maximum int64) bool {
	return n != nil && minimum <= maximum && n.IsInteger() &&
		n.CmpInt64(minimum) >= 0 && n.CmpInt64(maximum) <= 0
}

func compareMagnitude(left, right *Number) int {
	leftDigits := decimalDigits(&left.coefficient)
	rightDigits := decimalDigits(&right.coefficient)

	var leftMagnitude, rightMagnitude big.Int
	leftMagnitude.Add(&left.exponent, big.NewInt(int64(leftDigits)))
	rightMagnitude.Add(&right.exponent, big.NewInt(int64(rightDigits)))
	if cmp := leftMagnitude.Cmp(&rightMagnitude); cmp != 0 {
		return cmp
	}

	// Equal decimal magnitudes bound the exponent difference by the digit
	// counts of the two already-materialized coefficients.
	if cmp := left.exponent.Cmp(&right.exponent); cmp == 0 {
		return absInt(&left.coefficient).Cmp(absInt(&right.coefficient))
	} else if cmp > 0 {
		difference := rightDigits - leftDigits
		return appendDecimalZeros(&left.coefficient, difference).
			Cmp(absInt(&right.coefficient))
	}
	difference := leftDigits - rightDigits
	return absInt(&left.coefficient).
		Cmp(appendDecimalZeros(&right.coefficient, difference))
}

func decimalDigits(value *big.Int) int {
	return len(absInt(value).String())
}

func absInt(value *big.Int) *big.Int {
	return new(big.Int).Abs(value)
}

func appendDecimalZeros(value *big.Int, count int) *big.Int {
	if count <= 0 {
		return absInt(value)
	}
	power := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(count)), nil)
	return new(big.Int).Mul(absInt(value), power)
}

func numberFromInt64(value int64) *Number {
	n := &Number{resolution: IntegerResolution, spelling: new(big.Int).SetInt64(value).String()}
	n.coefficient.SetInt64(value)
	n.normalize()
	return n
}

func (n *Number) normalize() {
	if n.coefficient.Sign() == 0 {
		n.exponent.SetInt64(0)
		return
	}

	negative := n.coefficient.Sign() < 0
	digits := new(big.Int).Abs(&n.coefficient).String()
	trimmed := strings.TrimRight(digits, "0")
	zeros := len(digits) - len(trimmed)
	if zeros == 0 {
		return
	}
	n.coefficient.SetString(trimmed, 10)
	if negative {
		n.coefficient.Neg(&n.coefficient)
	}
	n.exponent.Add(&n.exponent, big.NewInt(int64(zeros)))
}
