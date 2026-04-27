package roots

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// DirToURI converts an absolute directory path to a file:// URI.
func DirToURI(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
}

// CanonicalRootURI resolves a client-provided local file URI to the canonical
// absolute file URI we use as the server's internal root identity. Equivalent
// aliases such as file:///repo and file://localhost/repo collapse to the same
// canonical URI and directory.
func CanonicalRootURI(raw string) (string, string, error) {
	dir, err := fileURIToPath(raw)
	if err != nil {
		return "", "", err
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve path for %q: %w", raw, err)
	}

	return DirToURI(abs), abs, nil
}

// fileURIToPath parses a local file:// URI into a filesystem path.
func fileURIToPath(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URI %q: %w", raw, err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("unsupported URI scheme %q (only file:// is supported)", u.Scheme)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("file URI %q must not include query or fragment", raw)
	}
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		return "", fmt.Errorf("UNC file URI %q is not supported", raw)
	}

	path := u.Path
	if path == "" {
		return "", fmt.Errorf("file URI %q is missing a path", raw)
	}
	if isWindowsDriveURIPath(path) {
		if runtime.GOOS != "windows" {
			return "", fmt.Errorf("windows file URI %q is not supported on %s", raw, runtime.GOOS)
		}
		path = strings.TrimPrefix(path, "/")
	}

	return filepath.Clean(filepath.FromSlash(path)), nil
}

func isWindowsDriveURIPath(path string) bool {
	if len(path) < 3 || path[0] != '/' || path[2] != ':' {
		return false
	}

	drive := path[1]
	return (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')
}
