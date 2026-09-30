//go:build !windows

package yzma

// prepareLibrarySearchPath is a no-op off Windows.
//
// macOS and Linux resolve a library's own dependencies through RPATH/RUNPATH or
// by soname, so a side-by-side install needs no loader configuration.
func prepareLibrarySearchPath(string) error {
	return nil
}