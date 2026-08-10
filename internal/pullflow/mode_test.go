package pullflow

import (
	"runtime"
	"testing"
)

func TestEquivalentPathPermissionsMatchesHostRepresentation(t *testing.T) {
	t.Parallel()
	if !equivalentPathPermissions(0o644, 0o644) {
		t.Fatal("identical permissions are not equivalent")
	}
	if runtime.GOOS == "windows" {
		if !equivalentPathPermissions(0o644, 0o666) || !equivalentPathPermissions(0o755, 0o666) || equivalentPathPermissions(0o444, 0o666) {
			t.Fatal("Windows equivalence does not match its writable/read-only representation")
		}
	} else if equivalentPathPermissions(0o644, 0o666) || equivalentPathPermissions(0o755, 0o644) {
		t.Fatal("Unix equivalence accepted different permission bits")
	}
}

func TestSamePathImageUsesPortableModesAndExactBytes(t *testing.T) {
	t.Parallel()
	if !samePathImage(&pathImage{Kind: "directory", Mode: 0o700}, &pathImage{Kind: "directory", Mode: 0o755}) {
		t.Fatal("directory presentation modes changed the reconciled image")
	}
	left := &pathImage{Kind: "regular", Mode: 0o644, Data: []byte("left\n")}
	right := &pathImage{Kind: "regular", Mode: 0o644, Data: []byte("right\n")}
	if samePathImage(left, right) {
		t.Fatal("different regular-file bytes were treated as equivalent")
	}
}

func TestValidatePathImageRejectsOpenShapes(t *testing.T) {
	t.Parallel()
	invalid := []*pathImage{
		{Kind: "special", Mode: 0o600},
		{Kind: "regular", Mode: 0o1000},
		{Kind: "directory", Mode: 0o700, Data: []byte("unexpected")},
	}
	for _, image := range invalid {
		if err := validatePathImage(image); err == nil {
			t.Fatalf("invalid image accepted: %#v", image)
		}
	}
	if err := validatePathImage(&pathImage{Kind: "regular", Mode: 0o644, Data: []byte("ok")}); err != nil {
		t.Fatalf("valid image rejected: %v", err)
	}
}
