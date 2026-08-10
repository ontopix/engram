package managedread

import (
	"bytes"
	"testing"
)

func TestWriteLogTextSeparatesCommitsWithoutTrailingBlankRecord(t *testing.T) {
	t.Parallel()
	result := &LogResult{Commits: []CommitView{
		{ID: "newest", Parents: []string{"oldest"}, Message: "first\n"},
		{ID: "oldest", Parents: []string{}, Message: "second\n"},
	}}
	var output bytes.Buffer
	if err := WriteLogText(&output, result, LogTextFull); err != nil {
		t.Fatal(err)
	}
	want := "commit newest\n" +
		"Parents: oldest\nAuthor: <unknown>\nAuthorDate: <unknown>\nCommitter: <unknown>\nCommitDate: <unknown>\nMessage:\n    first\n\n" +
		"commit oldest\n" +
		"Parents: <none>\nAuthor: <unknown>\nAuthorDate: <unknown>\nCommitter: <unknown>\nCommitDate: <unknown>\nMessage:\n    second\n"
	if output.String() != want {
		t.Fatalf("log text = %q, want %q", output.String(), want)
	}
}
