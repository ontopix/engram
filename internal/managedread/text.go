package managedread

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// DiffTextMode selects only the human presentation of a complete DiffResult.
// JSON callers continue to receive the same selectors, changes, and counts.
type DiffTextMode uint8

const (
	DiffTextContent DiffTextMode = iota
	DiffTextStat
	DiffTextNames
)

// WriteDiffText renders the complete logical diff in the requested human
// form. Content mode uses whole-file unified hunks; this keeps the renderer
// deterministic without changing the protocol changeset into a line model.
func WriteDiffText(output io.Writer, result *DiffResult, mode DiffTextMode) error {
	if output == nil || result == nil {
		return fmt.Errorf("render diff: missing output or result")
	}
	switch mode {
	case DiffTextStat:
		_, err := fmt.Fprintf(output, "added: %d\nmodified: %d\ndeleted: %d\n", result.Stat.Added, result.Stat.Modified, result.Stat.Deleted)
		return err
	case DiffTextNames:
		for _, change := range result.Changes {
			if _, err := fmt.Fprintln(output, change.Path); err != nil {
				return err
			}
		}
		return nil
	case DiffTextContent:
		if len(result.textFiles) != len(result.Changes) {
			return fmt.Errorf("render diff: content does not match logical changes")
		}
		for _, file := range result.textFiles {
			if err := writeDiffFile(output, file); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("render diff: unknown text mode %d", mode)
	}
}

func writeDiffFile(output io.Writer, file diffTextFile) error {
	beforeName := "/dev/null"
	if file.beforePresent {
		beforeName = "a/" + file.path
	}
	afterName := "/dev/null"
	if file.afterPresent {
		afterName = "b/" + file.path
	}
	if _, err := fmt.Fprintf(output, "diff --engram %s\n--- %s\n+++ %s\n", file.path, beforeName, afterName); err != nil {
		return err
	}
	if !textDiffable(file.before) || !textDiffable(file.after) {
		_, err := fmt.Fprintf(output, "Binary files %s and %s differ\n", beforeName, afterName)
		return err
	}
	if _, err := fmt.Fprintf(output, "@@ -%s +%s @@\n", unifiedRange(file.before, file.beforePresent), unifiedRange(file.after, file.afterPresent)); err != nil {
		return err
	}
	if err := writePrefixedLines(output, '-', file.before); err != nil {
		return err
	}
	return writePrefixedLines(output, '+', file.after)
}

func textDiffable(data []byte) bool {
	return utf8.Valid(data) && !bytes.ContainsRune(data, '\x00')
}

func unifiedRange(data []byte, present bool) string {
	if !present || len(data) == 0 {
		return "0,0"
	}
	lines := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		lines++
	}
	return fmt.Sprintf("1,%d", lines)
}

func writePrefixedLines(output io.Writer, prefix byte, data []byte) error {
	for len(data) != 0 {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			if _, err := output.Write([]byte{prefix}); err != nil {
				return err
			}
			if _, err := output.Write(data); err != nil {
				return err
			}
			_, err := io.WriteString(output, "\n\\ No newline at end of file\n")
			return err
		}
		end++
		if _, err := output.Write([]byte{prefix}); err != nil {
			return err
		}
		if _, err := output.Write(data[:end]); err != nil {
			return err
		}
		data = data[end:]
	}
	return nil
}

// LogTextMode selects the full or one-line human history presentation.
type LogTextMode uint8

const (
	LogTextFull LogTextMode = iota
	LogTextOneline
)

// WriteLogText renders newest-first history without altering LogResult's JSON
// representation.
func WriteLogText(output io.Writer, result *LogResult, mode LogTextMode) error {
	if output == nil || result == nil {
		return fmt.Errorf("render log: missing output or result")
	}
	for _, commit := range result.Commits {
		switch mode {
		case LogTextOneline:
			subject, _, _ := strings.Cut(commit.Message, "\n")
			if _, err := fmt.Fprintf(output, "%s %s\n", commit.ID, subject); err != nil {
				return err
			}
		case LogTextFull:
			if err := writeFullCommit(output, commit); err != nil {
				return err
			}
		default:
			return fmt.Errorf("render log: unknown text mode %d", mode)
		}
	}
	return nil
}

func writeFullCommit(output io.Writer, commit CommitView) error {
	if _, err := fmt.Fprintf(output, "commit %s\n", commit.ID); err != nil {
		return err
	}
	parents := "<none>"
	if len(commit.Parents) != 0 {
		parents = strings.Join(commit.Parents, " ")
	}
	if _, err := fmt.Fprintf(output, "Parents: %s\nAuthor: %s\nAuthorDate: %s\nCommitter: %s\nCommitDate: %s\nMessage:\n",
		parents, formatIdentity(commit.Author), formatOptional(commit.AuthoredAt), formatIdentity(commit.Committer), formatOptional(commit.CommittedAt)); err != nil {
		return err
	}
	message := strings.TrimSuffix(commit.Message, "\n")
	for _, line := range strings.Split(message, "\n") {
		if _, err := fmt.Fprintf(output, "    %s\n", line); err != nil {
			return err
		}
	}
	_, err := io.WriteString(output, "\n")
	return err
}

func formatIdentity(identity *Identity) string {
	if identity == nil {
		return "<unknown>"
	}
	return identity.Name + " <" + identity.Email + ">"
}

func formatOptional(value *string) string {
	if value == nil {
		return "<unknown>"
	}
	return *value
}
