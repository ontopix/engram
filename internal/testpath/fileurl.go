// Package testpath contains host-portable path encodings used by integration
// fixtures. It is intentionally imported only by tests.
package testpath

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// FileURL encodes one native absolute path as a valid file URL. A Windows
// drive path needs both slash translation and the URL path's leading slash.
func FileURL(name string) string {
	path := filepath.ToSlash(name)
	if runtime.GOOS == "windows" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}
