package lsp

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// pathToURI converts a filesystem path to an LSP document URI.
func pathToURI(path string) string {
	path = filepath.Clean(path)
	if path == "" {
		return ""
	}
	// Ensure absolute for URI conversion when possible.
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.ToSlash(path)

	if runtime.GOOS == "windows" {
		// file:///C:/path
		if len(path) >= 2 && path[1] == ':' {
			return "file:///" + path
		}
		if strings.HasPrefix(path, "//") {
			// UNC: //server/share -> file://server/share
			return "file:" + path
		}
		return "file:///" + path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "file://" + path
}

// uriToPath converts an LSP document URI back to a filesystem path.
func uriToPath(uri string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	if u.Scheme != "file" {
		return uri
	}
	path := u.Path
	if runtime.GOOS == "windows" {
		// /C:/foo -> C:/foo
		if strings.HasPrefix(path, "/") && len(path) >= 3 && path[2] == ':' {
			path = path[1:]
		}
		// UNC host
		if u.Host != "" {
			path = "//" + u.Host + path
		}
		return filepath.FromSlash(path)
	}
	if u.Host != "" && u.Host != "localhost" {
		return filepath.FromSlash("//" + u.Host + path)
	}
	return filepath.FromSlash(path)
}
