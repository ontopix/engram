// Package gitraw reads and validates the raw Git representation used by the
// normative managed-store annex. It never delegates ancestry or tree
// projection to Git's revision walker.
package gitraw

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
)

type ObjectFormat string

const (
	SHA1   ObjectFormat = "sha1"
	SHA256 ObjectFormat = "sha256"
)

func ParseObjectFormat(value string) (ObjectFormat, error) {
	format := ObjectFormat(value)
	if format != SHA1 && format != SHA256 {
		return "", &Error{Kind: FailureCapability, Op: "object-format", Detail: fmt.Sprintf("unsupported object format %q", value)}
	}
	return format, nil
}

func (f ObjectFormat) RawWidth() int {
	switch f {
	case SHA1:
		return 20
	case SHA256:
		return 32
	default:
		return 0
	}
}

func (f ObjectFormat) HexWidth() int { return f.RawWidth() * 2 }

type OID struct {
	format ObjectFormat
	hex    string
}

func ParseOID(format ObjectFormat, value string) (OID, error) {
	if len(value) != format.HexWidth() || !lowerHex(value) {
		return OID{}, &Error{Kind: FailureMalformed, Op: "object-id", Detail: fmt.Sprintf("object ID is not canonical %s", format)}
	}
	return OID{format: format, hex: value}, nil
}

func oidFromRaw(format ObjectFormat, value []byte) OID {
	return OID{format: format, hex: hex.EncodeToString(value)}
}

func (o OID) String() string       { return o.hex }
func (o OID) Format() ObjectFormat { return o.format }
func (o OID) Valid() bool          { return len(o.hex) == o.format.HexWidth() && lowerHex(o.hex) }
func (o OID) Equal(other OID) bool { return o == other }

func (o OID) Raw() []byte {
	decoded, _ := hex.DecodeString(o.hex)
	return decoded
}

func lowerHex(value string) bool {
	if value == "" {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if character >= '0' && character <= '9' {
			continue
		}
		if character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

type ObjectType string

const (
	TypeBlob   ObjectType = "blob"
	TypeTree   ObjectType = "tree"
	TypeCommit ObjectType = "commit"
	TypeTag    ObjectType = "tag"
)

type Object struct {
	OID  OID
	Type ObjectType
	Data []byte
}

type Reader interface {
	ReadObject(ctx context.Context, oid OID) (Object, error)
}

type FailureKind string

const (
	FailureMalformed  FailureKind = "malformed"
	FailureMissing    FailureKind = "missing"
	FailureWrongType  FailureKind = "wrong-type"
	FailureRepository FailureKind = "repository"
	FailureCapability FailureKind = "capability"
	FailureGit        FailureKind = "git"
	FailureIO         FailureKind = "io"
)

type Error struct {
	Kind   FailureKind
	Op     string
	OID    OID
	Detail string
	Err    error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	message := string(e.Kind)
	if e.Op != "" {
		message = e.Op + ": " + message
	}
	if e.OID.Valid() {
		message += " object " + e.OID.String()
	}
	if e.Detail != "" {
		message += ": " + e.Detail
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *Error) Unwrap() error { return e.Err }

func (e *Error) Is(target error) bool {
	var candidate *Error
	return errors.As(target, &candidate) && e.Kind == candidate.Kind
}

var (
	ErrMalformedObject = &Error{Kind: FailureMalformed}
	ErrMissingObject   = &Error{Kind: FailureMissing}
	ErrWrongObjectType = &Error{Kind: FailureWrongType}
	ErrRepository      = &Error{Kind: FailureRepository}
	ErrCapability      = &Error{Kind: FailureCapability}
)

func malformed(op string, offset int, format string, arguments ...any) error {
	detail := fmt.Sprintf(format, arguments...)
	if offset >= 0 {
		detail = fmt.Sprintf("byte %d: %s", offset, detail)
	}
	return &Error{Kind: FailureMalformed, Op: op, Detail: detail}
}

func wrongType(op string, oid OID, got, want ObjectType) error {
	return &Error{Kind: FailureWrongType, Op: op, OID: oid, Detail: fmt.Sprintf("got %s, want %s", got, want)}
}
