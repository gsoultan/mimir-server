package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ArchiveFiles writes the data folder to the tar.gz file at dst and returns
// how many files it holds. Paths in skip, files or folders, are left out,
// along with anything that is not a regular file or a folder (a symlink
// could point anywhere). The files are read as they are at the time, so
// for a copy that matches the database dump, take the dump first: a model
// file written after it is harmless, one missing from it is not.
func ArchiveFiles(dataDir, dst string, skip []string) (n int, err error) {
	root, err := filepath.Abs(dataDir)
	if err != nil {
		return 0, err
	}
	skipped := make(map[string]bool, len(skip))
	for _, p := range skip {
		if p == "" {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			skipped[abs] = true
		}
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, fmt.Errorf("backup: %w", err)
	}
	defer func() {
		if cerr := out.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("backup: %w", cerr)
		}
	}()
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	walk := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		if skipped[p] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.Uname, hdr.Gname, hdr.Uid, hdr.Gid = "", "", 0, 0
		if info.IsDir() {
			hdr.Name += "/"
			return tw.WriteHeader(hdr)
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		// A file that grows while it is read is cut at the size its
		// header promised; one that shrinks fails the backup.
		if _, err := io.CopyN(tw, f, hdr.Size); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		n++
		return nil
	})
	if err := errors.Join(walk, tw.Close(), gz.Close(), out.Sync()); err != nil {
		return n, fmt.Errorf("backup: archive the data folder: %w", err)
	}
	return n, nil
}

// ExtractFiles puts the files ArchiveFiles wrote back into dataDir and
// returns how many it wrote. Unless replace is set it first checks that
// none of them is there already, and writes nothing if one is. Entries
// that would land outside dataDir, and anything but files and folders,
// are refused.
func ExtractFiles(src, dataDir string, replace bool) (int, error) {
	if !replace {
		var clash []string
		err := eachEntry(src, func(name string, hdr *tar.Header, _ io.Reader) error {
			if hdr.Typeflag == tar.TypeDir {
				return nil
			}
			if _, err := os.Lstat(filepath.Join(dataDir, name)); err == nil {
				clash = append(clash, name)
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
		if len(clash) > 0 {
			more := ""
			if len(clash) > 3 {
				more = fmt.Sprintf(" and %d more", len(clash)-3)
				clash = clash[:3]
			}
			return 0, fmt.Errorf("backup: the data folder already has %s%s; restore into an empty one, or pass -replace", strings.Join(clash, ", "), more)
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return 0, fmt.Errorf("backup: %w", err)
	}
	n := 0
	err := eachEntry(src, func(name string, hdr *tar.Header, r io.Reader) error {
		dst := filepath.Join(dataDir, name)
		if hdr.Typeflag == tar.TypeDir {
			return os.MkdirAll(dst, 0o750)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return err
		}
		// Written beside the file and renamed over it, so a restore that
		// stops half way never leaves a cut-off model under its own name.
		tmp, err := os.CreateTemp(filepath.Dir(dst), ".restore-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		_, werr := io.Copy(tmp, r)
		cerr := tmp.Chmod(fs.FileMode(hdr.Mode) & 0o750)
		serr := tmp.Sync()
		if err := errors.Join(werr, cerr, serr, tmp.Close()); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := os.Rename(tmp.Name(), dst); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return n, err
	}
	return n, nil
}

// eachEntry calls fn for each entry of the tar.gz at src, with its name
// checked and made native, refusing any it shouldn't hold.
func eachEntry(src string, fn func(name string, hdr *tar.Header, r io.Reader) error) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("backup: %s: %w", filepath.Base(src), err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("backup: %s: %w", filepath.Base(src), err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			return fmt.Errorf("backup: %s holds %q, which is neither a file nor a folder", filepath.Base(src), hdr.Name)
		}
		name := path.Clean(strings.TrimSuffix(hdr.Name, "/"))
		if !fs.ValidPath(name) || name == "." || strings.Contains(hdr.Name, `\`) {
			return fmt.Errorf("backup: %s holds %q, which would land outside the data folder", filepath.Base(src), hdr.Name)
		}
		if err := fn(filepath.FromSlash(name), hdr, tr); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}
}
