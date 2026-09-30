//go:build windows

package yzma

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32DLL    = syscall.NewLazyDLL("kernel32.dll")
	procSetDllDir  = kernel32DLL.NewProc("SetDllDirectoryW")
	procAddDllDir  = kernel32DLL.NewProc("AddDllDirectory")
	procSetDefault = kernel32DLL.NewProc("SetDefaultDllDirectories")
)

func prepareLibrarySearchPath(dir string) error {
	utf16, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	// Note on the Call error convention: LazyProc.Call always returns a non-nil
	// error, including errno 0 ("The operation completed successfully"). The
	// only reliable success test is the returned handle/BOOL, never callErr.
	const (
		loadLibrarySearchDefaultDirs = 0x00001000
		loadLibrarySearchUserDirs    = 0x00000400
	)
	if r1, _, _ := procSetDefault.Call(uintptr(loadLibrarySearchDefaultDirs | loadLibrarySearchUserDirs)); r1 != 0 {
		if r2, _, _ := procAddDllDir.Call(uintptr(unsafe.Pointer(utf16))); r2 != 0 {
			return nil
		}
	}
	// Fallback for systems without SetDefaultDllDirectories/AddDllDirectory.
	// SetDllDirectoryW returns a non-zero BOOL on success.
	if r1, _, callErr := procSetDllDir.Call(uintptr(unsafe.Pointer(utf16))); r1 == 0 {
		return fmt.Errorf("SetDllDirectoryW(%s): %w", dir, callErr)
	}
	return nil
}