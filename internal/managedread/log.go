package managedread

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/gitraw"
)

// Identity is the nullable historical Git identity model used by log.
type Identity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// CommitView is the deterministic protocol-facing representation of one raw
// accepted commit. Parents preserve their physical header order.
type CommitView struct {
	ID          string    `json:"id"`
	Parents     []string  `json:"parents"`
	Author      *Identity `json:"author"`
	Committer   *Identity `json:"committer"`
	AuthoredAt  *string   `json:"authored_at"`
	CommittedAt *string   `json:"committed_at"`
	Message     string    `json:"message"`
}

// LogResult is newest-first. MergeBoundary is diagnostic-only: the JSON
// result remains the closed commits-only shape while the caller maps it to an
// issues outcome.
type LogResult struct {
	Commits       []CommitView `json:"commits"`
	MergeBoundary *string      `json:"-"`
}

// Log walks raw parents itself without revision machinery, replacements,
// grafts, lazy fetching, or implicit network access. It stops at count or a
// root. A merge commit is included and terminates traversal before a parent is
// chosen.
func (s *Store) Log(ctx context.Context, count int) (*LogResult, error) {
	if s == nil || s.repository == nil {
		return nil, fmt.Errorf("managedread: nil store")
	}
	if count < 1 || int64(count) > 2147483647 {
		return nil, fmt.Errorf("log count must be between 1 and 2147483647")
	}
	result := &LogResult{Commits: make([]CommitView, 0, count)}
	if s.repository.Head == nil {
		return result, nil
	}
	seen := make(map[gitraw.OID]struct{})
	current := *s.repository.Head
	for len(result.Commits) < count {
		if _, duplicate := seen[current]; duplicate {
			return nil, &gitraw.Error{Kind: gitraw.FailureMalformed, Op: "log", OID: current, Detail: "raw parent cycle"}
		}
		seen[current] = struct{}{}
		object, err := s.repository.ReadObject(ctx, current)
		if err != nil {
			return nil, err
		}
		if object.Type != gitraw.TypeCommit {
			return nil, &gitraw.Error{
				Kind:   gitraw.FailureWrongType,
				Op:     "log",
				OID:    current,
				Detail: fmt.Sprintf("got %s, want %s", object.Type, gitraw.TypeCommit),
			}
		}
		commit, err := gitraw.ParseCommit(s.repository.Format, object.Data)
		if err != nil {
			return nil, err
		}
		view, err := commitView(current, commit)
		if err != nil {
			return nil, err
		}
		result.Commits = append(result.Commits, view)
		if len(commit.Parents) > 1 {
			result.MergeBoundary = stringPointer(current.String())
			break
		}
		if len(commit.Parents) == 0 {
			break
		}
		current = commit.Parents[0]
	}
	return result, nil
}

func commitView(id gitraw.OID, commit *gitraw.Commit) (CommitView, error) {
	result := CommitView{
		ID:      id.String(),
		Parents: make([]string, len(commit.Parents)),
	}
	for index, parent := range commit.Parents {
		result.Parents[index] = parent.String()
	}
	for _, header := range commit.Headers {
		if len(header.Continuations) != 0 {
			continue
		}
		switch header.Name {
		case "author":
			if result.Author == nil && result.AuthoredAt == nil {
				result.Author, result.AuthoredAt = parseSignature(header.Value)
			}
		case "committer":
			if result.Committer == nil && result.CommittedAt == nil {
				result.Committer, result.CommittedAt = parseSignature(header.Value)
			}
		}
	}
	message, err := decodeCommitMessage(commit)
	if err != nil {
		return CommitView{}, err
	}
	result.Message = message
	return result, nil
}

func parseSignature(value []byte) (*Identity, *string) {
	if !utf8.Valid(value) {
		return nil, nil
	}
	text := string(value)
	timeSpace := strings.LastIndexByte(text, ' ')
	if timeSpace < 0 {
		return nil, nil
	}
	stampSpace := strings.LastIndexByte(text[:timeSpace], ' ')
	if stampSpace < 0 {
		return nil, nil
	}
	identityText := text[:stampSpace]
	stampText := text[stampSpace+1 : timeSpace]
	zoneText := text[timeSpace+1:]

	var identity *Identity
	if strings.HasSuffix(identityText, ">") {
		open := strings.LastIndex(identityText[:len(identityText)-1], " <")
		if open >= 0 {
			identity = &Identity{
				Name:  identityText[:open],
				Email: identityText[open+2 : len(identityText)-1],
			}
		}
	}

	seconds, err := strconv.ParseInt(stampText, 10, 64)
	if err != nil || len(zoneText) != 5 || zoneText[0] != '+' && zoneText[0] != '-' {
		return identity, nil
	}
	hours, hourErr := strconv.Atoi(zoneText[1:3])
	minutes, minuteErr := strconv.Atoi(zoneText[3:5])
	if hourErr != nil || minuteErr != nil || hours > 23 || minutes > 59 {
		return identity, nil
	}
	offset := hours*60*60 + minutes*60
	if zoneText[0] == '-' {
		offset = -offset
	}
	instant := time.Unix(seconds, 0).In(time.FixedZone("", offset))
	if instant.Year() < 1 || instant.Year() > 9999 {
		return identity, nil
	}
	formatted := instant.Format(time.RFC3339)
	return identity, stringPointer(formatted)
}

func decodeCommitMessage(commit *gitraw.Commit) (string, error) {
	encoding := ""
	for _, header := range commit.Headers {
		if header.Name == "encoding" && len(header.Continuations) == 0 {
			encoding = strings.ToLower(string(header.Value))
			break
		}
	}
	switch encoding {
	case "", "utf-8", "utf8":
		if !utf8.Valid(commit.Message) {
			return "", &gitraw.Error{Kind: gitraw.FailureCapability, Op: "decode-commit-message", Detail: "commit message is not valid UTF-8"}
		}
		return string(commit.Message), nil
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1":
		runes := make([]rune, len(commit.Message))
		for index, value := range commit.Message {
			runes[index] = rune(value)
		}
		return string(runes), nil
	default:
		return "", &gitraw.Error{Kind: gitraw.FailureCapability, Op: "decode-commit-message", Detail: fmt.Sprintf("unsupported historical encoding %q", encoding)}
	}
}
