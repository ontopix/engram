package checker

import "testing"

func TestNormedTextAndFrontmatter(t *testing.T) {
	t.Parallel()
	valid := []byte("---\ntype: note\ndescription: ok\n---\nbody\n")
	if !normedText(valid) {
		t.Fatal("valid text rejected")
	}
	document, body, err := frontmatter(valid)
	if err != nil {
		t.Fatal(err)
	}
	if value, _ := stringField(document.Root, "type"); value != "note" || string(body) != "body\n" {
		t.Fatalf("unexpected parse: type=%q body=%q", value, body)
	}
	for _, invalid := range [][]byte{
		{}, []byte("text"), []byte("text\r\n"), {0xef, 0xbb, 0xbf, 'x', '\n'}, {0xff, '\n'},
	} {
		if normedText(invalid) {
			t.Fatalf("invalid text accepted: %q", invalid)
		}
	}
}

func TestDescription(t *testing.T) {
	t.Parallel()
	if !validDescription("one line") {
		t.Fatal("valid description rejected")
	}
	for _, invalid := range []string{"", " edge", "edge ", "line\nbreak"} {
		if validDescription(invalid) {
			t.Fatalf("invalid description accepted: %q", invalid)
		}
	}
}
