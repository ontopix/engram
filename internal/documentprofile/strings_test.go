package documentprofile

import (
	"reflect"
	"testing"
)

func TestStringValuesRecursiveSourceOrderAndPointers(t *testing.T) {
	t.Parallel()
	document := parseYAML(t, `plain: one
mapping:
  key/with~syntax: two
  key-that-is-not-a-value:
    - three
    - 4
sequence:
  - nested: four
  - five
`)
	values := StringValues(document.Root)
	got := make([][2]string, len(values))
	for index := range values {
		got[index] = [2]string{values[index].Pointer, values[index].Value}
		if values[index].Node == nil || values[index].Position.Line == 0 {
			t.Fatalf("value %d lacks node/position: %#v", index, values[index])
		}
	}
	want := [][2]string{
		{"/plain", "one"},
		{"/mapping/key~1with~0syntax", "two"},
		{"/mapping/key-that-is-not-a-value/0", "three"},
		{"/sequence/0/nested", "four"},
		{"/sequence/1", "five"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("values = %#v, want %#v", got, want)
	}
}

func TestWalkStringValuesCanStop(t *testing.T) {
	t.Parallel()
	document := parseYAML(t, "a: one\nb: two\nc: three\n")
	var got []string
	WalkStringValues(document.Root, func(value StringValue) bool {
		got = append(got, value.Value)
		return len(got) < 2
	})
	if want := []string{"one", "two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("visited = %v, want %v", got, want)
	}
}
