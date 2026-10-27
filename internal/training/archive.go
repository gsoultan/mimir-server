package training

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/gsoultan/mimir-server/internal/schema"
)

// Limits on uploaded photo archives.
const (
	MaxArchiveFiles = 100_000
	MaxPhotos       = 30_000
	MaxArchiveBytes = 200 << 20
)

// Archive is an uploaded ZIP of photos: photos sorted into one folder per
// answer, or a data.csv with the photos it names.
type Archive struct {
	files map[string]*zip.File // by path, without a folder every file shares
}

// OpenArchive reads a ZIP's list of files.
func OpenArchive(b []byte) (*Archive, error) {
	if len(b) > MaxArchiveBytes {
		return nil, fmt.Errorf("%w: ZIP files up to %d MB work here", ErrBadTable, MaxArchiveBytes>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, fmt.Errorf("%w: that isn't a ZIP file", ErrBadTable)
	}
	if len(zr.File) > MaxArchiveFiles {
		return nil, fmt.Errorf("%w: at most %d files in one ZIP", ErrBadTable, MaxArchiveFiles)
	}
	var names []string
	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		name := path.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
		if f.FileInfo().IsDir() || hidden(name) {
			continue
		}
		byName[name] = f
		names = append(names, name)
	}
	// Zipping a folder puts everything under that folder's name; drop it.
	for {
		top := ""
		same := len(names) > 0
		for _, n := range names {
			first, _, ok := strings.Cut(n, "/")
			if !ok || top != "" && first != top {
				same = false
				break
			}
			top = first
		}
		if !same || !onlyNested(names, top) {
			break
		}
		next := map[string]*zip.File{}
		for i, n := range names {
			names[i] = strings.TrimPrefix(n, top+"/")
			next[names[i]] = byName[n]
		}
		byName = next
	}
	return &Archive{files: byName}, nil
}

// onlyNested reports whether dropping the shared folder top still leaves
// something useful: a data.csv, or photos inside folders.
func onlyNested(names []string, top string) bool {
	for _, n := range names {
		rest := strings.TrimPrefix(n, top+"/")
		if strings.Contains(rest, "/") || strings.EqualFold(path.Ext(rest), ".csv") {
			return true
		}
	}
	return false
}

// hidden reports files a computer adds to a ZIP by itself.
func hidden(name string) bool {
	for part := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(part, ".") || part == "__MACOSX" || strings.EqualFold(part, "Thumbs.db") {
			return true
		}
	}
	return false
}

// Folders returns the photos in each top folder, by folder name, and how
// many photos sit outside any folder.
func (a *Archive) Folders() (folders map[string][]string, loose int) {
	return a.FoldersOf(schema.IsPhotoName)
}

// FoldersOf returns the files is picks in each top folder, by folder
// name, and how many it picks outside any folder.
func (a *Archive) FoldersOf(is func(name string) bool) (folders map[string][]string, loose int) {
	folders = map[string][]string{}
	for name := range a.files {
		if !is(name) {
			continue
		}
		dir, _, ok := strings.Cut(name, "/")
		if !ok {
			loose++
			continue
		}
		folders[dir] = append(folders[dir], name)
	}
	for _, fs := range folders {
		slices.Sort(fs)
	}
	return folders, loose
}

// Table returns the CSV in the archive: data.csv, or else the only CSV.
func (a *Archive) Table() (name string, b []byte, err error) {
	var csvs []string
	for n := range a.files {
		if strings.EqualFold(path.Ext(n), ".csv") {
			csvs = append(csvs, n)
		}
	}
	slices.SortFunc(csvs, func(x, y string) int { return strings.Count(x, "/") - strings.Count(y, "/") })
	i := slices.IndexFunc(csvs, func(n string) bool { return strings.EqualFold(path.Base(n), "data.csv") })
	switch {
	case i >= 0:
		name = csvs[i]
	case len(csvs) == 1:
		name = csvs[0]
	case len(csvs) == 0:
		return "", nil, fmt.Errorf("%w: the ZIP needs a data.csv next to the photos folder", ErrBadTable)
	default:
		return "", nil, fmt.Errorf("%w: the ZIP has several tables; name the one to learn from data.csv", ErrBadTable)
	}
	b, err = a.read(name, 64<<20)
	return name, b, err
}

// Find returns the path of the photo a table cell names, looked up next
// to the table, in a photos folder, then by file name alone. It returns
// "" when there is no such photo.
func (a *Archive) Find(tableName, cell string) string {
	cell = strings.TrimSpace(strings.ReplaceAll(cell, "\\", "/"))
	if cell == "" {
		return ""
	}
	dir := path.Dir(tableName)
	for _, p := range []string{path.Join(dir, cell), path.Join(dir, "photos", cell), path.Clean(cell)} {
		if _, ok := a.files[p]; ok && schema.IsPhotoName(p) {
			return p
		}
	}
	base := path.Base(cell)
	for n := range a.files {
		if path.Base(n) == base && schema.IsPhotoName(n) {
			return n
		}
	}
	return ""
}

// Photo reads and shrinks one photo.
func (a *Archive) Photo(name string) ([]uint8, error) {
	b, err := a.read(name, schema.MaxPhotoBytes)
	if err != nil {
		return nil, err
	}
	return schema.DecodePhoto(b)
}

// Sound returns a sound's samples (see schema.DecodeSound).
func (a *Archive) Sound(name string) ([]int16, error) {
	b, err := a.read(name, schema.MaxSoundBytes)
	if err != nil {
		return nil, err
	}
	return schema.DecodeSound(b)
}

// read returns a file's bytes, refusing more than limit.
func (a *Archive) read(name string, limit int64) ([]byte, error) {
	f, ok := a.files[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s is missing from the ZIP", ErrBadTable, name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: %s can't be read: %v", ErrBadTable, name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %s can't be read: %v", ErrBadTable, name, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: %s is too big", ErrBadTable, name)
	}
	return b, nil
}
