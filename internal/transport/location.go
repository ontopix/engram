// Package transport validates the closed repository-location surface used by
// clone and computes its deterministic default destination. It performs no
// network or Git operation.
package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

// ValidateLocation accepts only the transports enumerated by CLI contract
// §4.2. The exact input is retained as identity; this function does not
// normalize, expand, or percent-decode it.
func ValidateLocation(location string) error {
	if location == "" || !utf8.ValidString(location) || strings.HasPrefix(location, "-") || hasControl(location) {
		return fmt.Errorf("repository location is empty, option-like, or not safe UTF-8")
	}
	if strings.HasPrefix(location, "https://") || strings.HasPrefix(location, "ssh://") || strings.HasPrefix(location, "file://") {
		return validateURL(location)
	}
	for _, forbidden := range []string{"http://", "git://", "ext::"} {
		if strings.HasPrefix(location, forbidden) {
			return fmt.Errorf("repository transport %q is not admitted", strings.TrimSuffix(forbidden, "://"))
		}
	}
	if strings.Contains(location, "://") || strings.Contains(location, "::") {
		return fmt.Errorf("unknown repository transport")
	}
	return validateSCP(location)
}

func validateURL(location string) error {
	parsed, err := url.Parse(location)
	if err != nil || parsed.Scheme == "" {
		return fmt.Errorf("invalid repository URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("repository URL cannot contain a query or fragment")
	}
	switch parsed.Scheme {
	case "https", "ssh":
		if parsed.Host == "" || parsed.Path == "" || parsed.Path == "/" {
			return fmt.Errorf("repository URL requires a host and path")
		}
		if parsed.User != nil && parsed.User.String() == "" {
			return fmt.Errorf("repository URL has an empty user")
		}
	case "file":
		if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("file URL has forbidden authority components")
		}
		if parsed.Host != "" && parsed.Host != "localhost" {
			return fmt.Errorf("file URL host must be empty or localhost")
		}
		if parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") {
			return fmt.Errorf("file URL must contain an absolute path")
		}
	default:
		return fmt.Errorf("repository URL scheme is not admitted")
	}
	return nil
}

func validateSCP(location string) error {
	colon := strings.IndexByte(location, ':')
	if colon <= 0 || colon == len(location)-1 {
		return fmt.Errorf("repository location is not an admitted URL or scp form")
	}
	left, remotePath := location[:colon], location[colon+1:]
	if strings.ContainsAny(remotePath, "\r\n\x00") {
		return fmt.Errorf("invalid scp repository path")
	}
	user, host := "", left
	if at := strings.IndexByte(left, '@'); at >= 0 {
		if at == 0 || at == len(left)-1 || strings.IndexByte(left[at+1:], '@') >= 0 {
			return fmt.Errorf("invalid scp repository authority")
		}
		user, host = left[:at], left[at+1:]
	}
	if host == "" || strings.ContainsAny(host, "/\\ ") || strings.ContainsAny(user, "/\\: ") {
		return fmt.Errorf("invalid scp repository authority")
	}
	return nil
}

// DefaultDestination derives the controller-owned clone destination from the
// exact location argument. Environment values are interpreted only as host
// directory configuration; they do not alter repository identity.
func DefaultDestination(location string) (string, error) {
	if err := ValidateLocation(location); err != nil {
		return "", err
	}
	root, err := dataRoot(runtime.GOOS, os.Getenv, os.UserHomeDir)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(location))
	return filepath.Join(root, "engram", "stores", hex.EncodeToString(digest[:])), nil
}

func dataRoot(goos string, getenv func(string) string, home func() (string, error)) (string, error) {
	switch goos {
	case "darwin":
		value, err := home()
		if err != nil || value == "" {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return filepath.Join(value, "Library", "Application Support"), nil
	case "windows":
		value := getenv("LOCALAPPDATA")
		if value == "" {
			return "", fmt.Errorf("LOCALAPPDATA is not set")
		}
		return value, nil
	default:
		if value := getenv("XDG_DATA_HOME"); value != "" {
			if !filepath.IsAbs(value) {
				return "", fmt.Errorf("XDG_DATA_HOME must be absolute")
			}
			return value, nil
		}
		value, err := home()
		if err != nil || value == "" {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return filepath.Join(value, ".local", "share"), nil
	}
}

func hasControl(value string) bool {
	for _, character := range value {
		if character <= 0x1f || character == 0x7f {
			return true
		}
	}
	return false
}
