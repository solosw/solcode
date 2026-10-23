package bundle

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Preserve the native runtime, executable bits and in-bundle symlinks.
// Never follow archive-created symlinks during extraction. Limits allow the
// single native executable, with a hard bound on unpacked content.
func extract(ctx context.Context, data []byte, zipped bool, root string) error {
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rootFS.Close()
	type link struct{ path, target string }
	var links []link
	var total int64
	entries := 0
	write := func(name string, mode os.FileMode, size int64, reader io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > 1000 || size < 0 || size > 128<<20 || total+size > 128<<20 {
			return fmt.Errorf("archive exceeds extraction limits")
		}
		total += size
		// macOS resource forks are not part of the native runtime.
		if strings.HasPrefix(name, "__MACOSX/") {
			return nil
		}
		if strings.Contains(name, "\\") || strings.Contains(name, ":") {
			return fmt.Errorf("invalid archive path %q", name)
		}
		name = strings.TrimPrefix(name, "./")
		clean := filepath.Clean(filepath.FromSlash(name))
		if !filepath.IsLocal(clean) {
			return fmt.Errorf("unsafe archive path %q", name)
		}
		dest := clean
		if mode.IsDir() {
			return rootFS.MkdirAll(dest, 0o755)
		}
		if mode&os.ModeSymlink != 0 {
			raw, err := io.ReadAll(io.LimitReader(reader, 4097))
			if err != nil {
				return err
			}
			target := string(raw)
			if len(raw) > 4096 || target == "" || strings.ContainsAny(target, "\\:") || filepath.IsAbs(target) || !filepath.IsLocal(filepath.Join(filepath.Dir(clean), target)) {
				return fmt.Errorf("unsafe archive symlink %q", name)
			}
			links = append(links, link{dest, target})
			return nil
		}
		if !mode.IsRegular() {
			return fmt.Errorf("unsupported archive entry %q", name)
		}
		if err := rootFS.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		file, err := rootFS.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600|mode.Perm()&0o155)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, io.LimitReader(reader, size+1))
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	if zipped {
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return err
		}
		for _, entry := range archive.File {
			reader, err := entry.Open()
			if err != nil {
				return err
			}
			err = write(entry.Name, entry.Mode(), int64(entry.UncompressedSize64), reader)
			_ = reader.Close()
			if err != nil {
				return err
			}
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return err
		}
		defer gz.Close()
		archive := tar.NewReader(gz)
		for {
			header, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			reader := io.Reader(archive)
			size := header.Size
			if header.Typeflag == tar.TypeSymlink {
				reader = strings.NewReader(header.Linkname)
				size = int64(len(header.Linkname))
			}
			if err := write(header.Name, header.FileInfo().Mode(), size, reader); err != nil {
				return err
			}
		}
	}
	for _, link := range links {
		if err := rootFS.MkdirAll(filepath.Dir(link.path), 0o755); err != nil {
			return err
		}
		if err := rootFS.Symlink(link.target, link.path); err != nil {
			return err
		}
	}
	// Resolve every symlink within os.Root, rejecting chained escapes and
	// dangling targets without ever following them outside the extraction root.
	for _, link := range links {
		if _, err := rootFS.Stat(link.path); err != nil {
			return fmt.Errorf("unsafe archive symlink %q: %w", link.path, err)
		}
	}
	return nil
}
