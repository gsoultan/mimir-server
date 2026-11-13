// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0 OR PolyForm-Small-Business-1.0.0 OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is free to use for personal and other noncommercial purposes, for
// nonprofits, and for companies with fewer than 100 people and under
// 1,000,000 USD in yearly revenue, under the PolyForm Noncommercial and
// PolyForm Small Business licenses. Any other use needs a commercial
// license from Gembit Soultan Shirazi. See the LICENSE and LICENSING.md files
// at the root of this repository.

package convert

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/gsoultan/mimir-model/importer"

	"github.com/gsoultan/mimir-server/internal/mapfile"
)

// Bounds on a ZIP of a model folder: the side files are small JSON files,
// read into memory; the weights may be large and are read from the ZIP in
// place when stored, or through a temporary file when compressed.
const (
	maxZipEntries = 1 << 12
	maxSideFile   = 64 << 20
)

// huggingFace reads a ZIP of a Hugging Face Transformers model folder:
// its config.json, the side files beside it and its safetensors weights,
// and describes the model with them (importer.FromHuggingFace). done
// releases what the weights were read from, once the source is written.
func huggingFace(b []byte) (src *importer.Source, done func(), err error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, nil, unsupported("the ZIP can't be read: %v", err)
	}
	if len(zr.File) > maxZipEntries {
		return nil, nil, unsupported("the ZIP holds %d files; a model folder has a few", len(zr.File))
	}
	// The folder is the shallowest one with a config.json.
	dir, found := "", false
	for _, f := range zr.File {
		if path.Base(f.Name) != "config.json" {
			continue
		}
		d := path.Dir(f.Name)
		if !found || strings.Count(d, "/") < strings.Count(dir, "/") {
			dir, found = d, true
		}
	}
	if !found {
		return nil, nil, unsupported("the ZIP has no config.json; ZIP the whole Hugging Face model folder")
	}
	in := func(f *zip.File) bool { return path.Dir(f.Name) == dir }
	var files importer.HFFiles
	var weights []*zip.File
	for _, f := range zr.File {
		if !in(f) || f.FileInfo().IsDir() {
			continue
		}
		var dst *[]byte
		switch path.Base(f.Name) {
		case "config.json":
			dst = &files.Config
		case "generation_config.json":
			dst = &files.GenerationConfig
		case "preprocessor_config.json":
			dst = &files.Preprocessor
		case "tokenizer.json":
			dst = &files.Tokenizer
		default:
			if strings.HasSuffix(f.Name, ".safetensors") {
				weights = append(weights, f)
			}
			continue
		}
		if f.UncompressedSize64 > maxSideFile {
			return nil, nil, unsupported("%s is %d MB, too large for a settings file", path.Base(f.Name), f.UncompressedSize64>>20)
		}
		r, err := f.Open()
		if err != nil {
			return nil, nil, unsupported("%s can't be read: %v", path.Base(f.Name), err)
		}
		*dst, err = io.ReadAll(io.LimitReader(r, maxSideFile))
		r.Close()
		if err != nil {
			return nil, nil, unsupported("%s can't be read: %v", path.Base(f.Name), err)
		}
	}
	if len(weights) == 0 {
		return nil, nil, unsupported("the folder has no .safetensors weights; download the model's safetensors files with it")
	}
	slices.SortFunc(weights, func(a, b *zip.File) int { return strings.Compare(a.Name, b.Name) })
	var closers []io.Closer
	release := func() {
		for _, c := range closers {
			c.Close()
		}
	}
	defer func() {
		if err != nil {
			release()
		}
	}()
	var srcs []*importer.Source
	for _, f := range weights {
		data, c, err := entryBytes(b, f)
		if err != nil {
			return nil, nil, err
		}
		if c != nil {
			closers = append(closers, c)
		}
		s, err := importer.ParseSafetensors(data)
		if err != nil {
			return nil, nil, unsupported("%s: %s", path.Base(f.Name), trim(err))
		}
		srcs = append(srcs, s)
	}
	if src, err = importer.Merge(srcs...); err != nil {
		return nil, nil, unsupported("%s", trim(err))
	}
	if _, err = importer.FromHuggingFace(src, files); err != nil {
		return nil, nil, unsupported("%s", strings.TrimPrefix(trim(err), "huggingface: "))
	}
	return src, release, nil
}

// entryBytes returns the bytes of a ZIP entry of b: a slice of b when the
// entry is stored as it is, else the entry uncompressed to a temporary
// file and mapped, which the returned closer unmaps and removes.
func entryBytes(b []byte, f *zip.File) ([]byte, io.Closer, error) {
	if f.Method == zip.Store {
		off, err := f.DataOffset()
		if err != nil || off < 0 || uint64(off)+f.UncompressedSize64 > uint64(len(b)) || f.CompressedSize64 != f.UncompressedSize64 {
			return nil, nil, unsupported("%s is damaged in the ZIP", path.Base(f.Name))
		}
		return b[off : uint64(off)+f.UncompressedSize64], nil, nil
	}
	r, err := f.Open()
	if err != nil {
		return nil, nil, unsupported("%s can't be read: %v", path.Base(f.Name), err)
	}
	defer r.Close()
	tmp, err := os.CreateTemp("", "mimir-unzip-*.safetensors")
	if err != nil {
		return nil, nil, fmt.Errorf("convert: %w", err)
	}
	name := tmp.Name()
	_, err = io.Copy(tmp, r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		if errors.Is(err, zip.ErrChecksum) || errors.Is(err, zip.ErrFormat) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil, unsupported("%s is damaged in the ZIP", path.Base(f.Name))
		}
		return nil, nil, fmt.Errorf("convert: %w", err)
	}
	m, err := mapfile.Open(name)
	if err != nil {
		os.Remove(name)
		return nil, nil, fmt.Errorf("convert: %w", err)
	}
	return m.Bytes(), removing{m, name}, nil
}

// removing unmaps a temporary file and removes it.
type removing struct {
	m    *mapfile.File
	name string
}

func (r removing) Close() error {
	err := r.m.Close()
	os.Remove(r.name)
	return err
}
