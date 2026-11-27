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

package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.4.0", "0.3.9", true},
		{"v0.4.0", "0.4.0", false},
		{"0.10.0", "0.9.1", true},
		{"1.0.0", "1.0.0-rc.1", true},
		{"1.0.0-rc.2", "1.0.0-rc.1", true},
		{"1.0.0-rc.10", "1.0.0-rc.9", true},
		{"1.0.0-rc.1", "1.0.0", false},
		{"0.4.0", "dev", true},
		{"dev", "0.4.0", false},
		{"0.4", "0.3.0", false},
		{"0.3.0", "0.4.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestDataDir(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "./data"},
		{[]string{"-addr", ":8080", "-data", "/var/lib/mimir"}, "/var/lib/mimir"},
		{[]string{"--data=/srv/m"}, "/srv/m"},
		{[]string{"backup", "-data", "/d"}, "/d"},
		{[]string{"-database", "x"}, "./data"},
	} {
		if got := DataDir(c.args); got != c.want {
			t.Errorf("DataDir(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

func keep(t *testing.T, data, version, content string) string {
	t.Helper()
	dir := Dir(data)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := binaryPrefix + version
	p := filepath.Join(dir, file)
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	if err := writeManifest(dir, manifest{Version: version, File: file, SHA256: hex.EncodeToString(sum[:]), From: "0.3.0"}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLaunchSwitchesToNewerKeptVersion(t *testing.T) {
	data := t.TempDir()
	p := keep(t, data, "0.4.0", "new")
	if got := Launch(data, "0.3.0", io.Discard); got != p {
		t.Fatalf("Launch = %q, want %q", got, p)
	}
	// The new version, once running, doesn't switch again.
	if got := Launch(data, "0.4.0", io.Discard); got != "" {
		t.Fatalf("Launch on the kept version = %q, want none", got)
	}
	from, err := Confirm(data, "0.4.0")
	if err != nil || from != "0.3.0" {
		t.Fatalf("Confirm = %q, %v; want 0.3.0", from, err)
	}
	if from, _ := Confirm(data, "0.4.0"); from != "" {
		t.Fatalf("a second Confirm = %q, want none", from)
	}
}

func TestLaunchSetsAsideAVersionThatNeverStarts(t *testing.T) {
	data := t.TempDir()
	p := keep(t, data, "0.4.0", "new")
	for i := range maxTries {
		if got := Launch(data, "0.3.0", io.Discard); got != p {
			t.Fatalf("try %d: Launch = %q, want %q", i+1, got, p)
		}
	}
	var msg bytes.Buffer
	if got := Launch(data, "0.3.0", &msg); got != "" {
		t.Fatalf("after %d tries Launch = %q, want none", maxTries, got)
	}
	if !strings.Contains(msg.String(), "didn't start") {
		t.Errorf("message = %q", msg.String())
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("the failed version is still kept: %v", err)
	}
}

func TestLaunchIgnoresAChangedFile(t *testing.T) {
	data := t.TempDir()
	p := keep(t, data, "0.4.0", "new")
	if err := os.WriteFile(p, []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Launch(data, "0.3.0", io.Discard); got != "" {
		t.Fatalf("Launch = %q, want none for a changed file", got)
	}
}

func TestLaunchDropsAVersionOlderThanTheInstalledOne(t *testing.T) {
	data := t.TempDir()
	p := keep(t, data, "0.4.0", "new")
	if got := Launch(data, "0.5.0", io.Discard); got != "" {
		t.Fatalf("Launch = %q, want none", got)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("the older kept version is still there: %v", err)
	}
}

// release serves a fake GitHub with one release whose archive holds
// program.
func release(t *testing.T, tag, asset string, program []byte, corrupt bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	dirName := strings.TrimSuffix(asset, ".tar.gz")
	_ = tw.WriteHeader(&tar.Header{Name: dirName + "/README.md", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.WriteHeader(&tar.Header{Name: dirName + "/mimir-server", Mode: 0o755, Size: int64(len(program)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(program)
	_ = tw.Close()
	_ = gz.Close()
	sum := sha256.Sum256(archive.Bytes())
	if corrupt {
		sum[0] ^= 1
	}
	downloads := new(atomic.Int32)
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/gsoultan/mimir-server/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": tag, "html_url": "https://github.com/gsoultan/mimir-server/releases/tag/" + tag,
				"published_at": time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
				"assets": []map[string]string{
					{"name": asset, "browser_download_url": srv.URL + "/dl/" + asset},
					{"name": "SHA256SUMS", "browser_download_url": srv.URL + "/dl/SHA256SUMS"},
				},
			})
		case "/dl/SHA256SUMS":
			fmt.Fprintf(w, "%s  other.tar.gz\n%s  %s\n", strings.Repeat("0", 64), hex.EncodeToString(sum[:]), asset)
		case "/dl/" + asset:
			downloads.Add(1)
			_, _ = w.Write(archive.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, downloads
}

func newUpdater(t *testing.T, srv *httptest.Server, current string) (*Updater, chan struct{}) {
	restarted := make(chan struct{}, 1)
	return &Updater{
		Current: current, DataDir: t.TempDir(), API: srv.URL, Client: srv.Client(),
		goos: "linux", goarch: "amd64", static: func() bool { return false },
		Restart: func() { restarted <- struct{}{} },
	}, restarted
}

func TestCheck(t *testing.T) {
	srv, _ := release(t, "v0.4.0", "mimir-server_v0.4.0_linux_amd64.tar.gz", []byte("x"), false)
	u, _ := newUpdater(t, srv, "0.3.0")
	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Available || st.Latest.Version != "0.4.0" || st.Blocked != NotBlocked || st.Checked.IsZero() {
		t.Fatalf("status = %+v", st)
	}
	u.Current = "0.4.0"
	if st := u.Status(); st.Available {
		t.Fatal("0.4.0 is the newest, yet an update is available")
	}
	u.Current = "dev"
	if st := u.Status(); st.Blocked != DevBuild {
		t.Fatalf("a dev build: blocked = %v", st.Blocked)
	}
	// A static build looks for the static archive, which this release lacks.
	u.Current, u.static = "0.3.0", func() bool { return true }
	if st := u.Status(); st.Blocked != NoDownload {
		t.Fatalf("a static build: blocked = %v", st.Blocked)
	}
}

func TestInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs a shell script as the new version")
	}
	program := []byte("#!/bin/sh\nexit 0\n")
	srv, downloads := release(t, "v0.4.0", "mimir-server_v0.4.0_linux_amd64.tar.gz", program, false)
	u, restarted := newUpdater(t, srv, "0.3.0")
	u.goos, u.goarch = runtime.GOOS, runtime.GOARCH
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("the fake release is for linux/amd64")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := u.InstallNow(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-restarted:
	case <-ctx.Done():
		t.Fatal("never restarted")
	}
	if downloads.Load() != 1 {
		t.Errorf("downloaded %d times", downloads.Load())
	}
	want := filepath.Join(Dir(u.DataDir), "mimir-server-0.4.0")
	if got := Launch(u.DataDir, "0.3.0", io.Discard); got != want {
		t.Fatalf("Launch after install = %q, want %q", got, want)
	}
	if b, _ := os.ReadFile(want); !bytes.Equal(b, program) {
		t.Fatalf("kept program = %q", b)
	}
	// Nothing is left over from the download.
	entries, _ := os.ReadDir(Dir(u.DataDir))
	if len(entries) != 2 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("updates folder holds %v", names)
	}
}

func TestInstallRefusesAMismatchedDownload(t *testing.T) {
	srv, _ := release(t, "v0.4.0", "mimir-server_v0.4.0_linux_amd64.tar.gz", []byte("#!/bin/sh\nexit 0\n"), true)
	u, restarted := newUpdater(t, srv, "0.3.0")
	err := u.InstallNow(context.Background())
	if err == nil || !strings.Contains(err.Error(), "doesn't match") {
		t.Fatalf("err = %v", err)
	}
	if st := u.Status(); st.State != Idle || st.InstallErr == "" {
		t.Fatalf("status = %+v", st)
	}
	select {
	case <-restarted:
		t.Fatal("restarted on a bad download")
	default:
	}
	if got := Launch(u.DataDir, "0.3.0", io.Discard); got != "" {
		t.Fatalf("Launch = %q after a refused download", got)
	}
}

func TestInstallRefusesAProgramThatDoesNotRun(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("the fake release is for linux/amd64")
	}
	srv, _ := release(t, "v0.4.0", "mimir-server_v0.4.0_linux_amd64.tar.gz", []byte("#!/bin/sh\nexit 3\n"), false)
	u, _ := newUpdater(t, srv, "0.3.0")
	err := u.InstallNow(context.Background())
	if err == nil || !strings.Contains(err.Error(), "doesn't run") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallWhenUpToDate(t *testing.T) {
	srv, _ := release(t, "v0.4.0", "mimir-server_v0.4.0_linux_amd64.tar.gz", []byte("x"), false)
	u, _ := newUpdater(t, srv, "0.4.0")
	if err := u.Install(context.Background()); err != ErrUpToDate {
		t.Fatalf("err = %v, want ErrUpToDate", err)
	}
}
