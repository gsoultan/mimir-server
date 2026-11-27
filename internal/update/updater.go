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
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultRepo is where mimir-server's releases are published.
	DefaultRepo = "gsoultan/mimir-server"
	// DefaultAPI is GitHub's API.
	DefaultAPI = "https://api.github.com"

	binaryPrefix = "mimir-server-"
	// The most a download may be, so a wrong file can't fill the disk.
	maxArchive = 2 << 30
	maxBinary  = 2 << 30
	maxSums    = 1 << 20
	maxRelease = 8 << 20
)

// apiURL is the default for Updater.API. A test build can point it at a
// copy of GitHub with -ldflags "-X ...update.apiURL=...".
var apiURL = DefaultAPI

// State is what the updater is doing.
type State int

const (
	Idle State = iota
	Checking
	Installing
	// Restarting: the new version is ready and the server is stopping to
	// start again on it.
	Restarting
)

// Block says why this server can't update itself.
type Block int

const (
	NotBlocked Block = iota
	// DevBuild: a development build has no release to compare with.
	DevBuild
	// NoDownload: the release has no file for this machine.
	NoDownload
)

// Release is one published version.
type Release struct {
	Version   string
	Tag       string
	URL       string
	Published time.Time
	// assets maps file names to download addresses.
	assets map[string]string
}

// Status is what the updater knows.
type Status struct {
	Current string
	// Latest is the newest release, nil until a check found one.
	Latest   *Release
	Checked  time.Time
	CheckErr string
	State    State
	// InstallErr is why the last install failed, until the next starts.
	InstallErr string
	// Available says Latest is newer than Current.
	Available bool
	Blocked   Block
}

// ErrBusy is returned while a check or install is under way.
var ErrBusy = errors.New("an update is already under way")

// ErrUpToDate is returned by Install when nothing newer is out.
var ErrUpToDate = errors.New("this is already the newest version")

// Updater checks for and installs new versions.
type Updater struct {
	// Current is the running version, such as "0.4.0"; anything else,
	// such as "dev", can check but never installs.
	Current string
	// DataDir is the server's data folder; new versions are kept in its
	// updates folder.
	DataDir string
	// Repo is the GitHub repository, owner/name; default DefaultRepo.
	Repo string
	// API is GitHub's API address; default DefaultAPI.
	API string
	// Client makes the requests; default one that follows the proxy
	// environment variables.
	Client *http.Client
	Log    *slog.Logger
	// Restart is called once a new version is kept and checked: it should
	// stop the server and start it again, which Launch then switches to
	// the new version.
	Restart func()

	// goos and goarch name the machine; tests set them.
	goos, goarch string
	static       func() bool

	mu         sync.Mutex
	latest     *Release
	checked    time.Time
	checkErr   string
	state      State
	installErr string
}

func (u *Updater) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return &http.Client{Transport: http.DefaultTransport}
}

func (u *Updater) log() *slog.Logger {
	if u.Log != nil {
		return u.Log
	}
	return slog.Default()
}

// Status says what the updater knows now.
func (u *Updater) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.statusLocked()
}

func (u *Updater) statusLocked() Status {
	s := Status{
		Current: strings.TrimPrefix(u.Current, "v"), Latest: u.latest, Checked: u.checked,
		CheckErr: u.checkErr, State: u.state, InstallErr: u.installErr,
	}
	if !IsRelease(u.Current) {
		s.Blocked = DevBuild
	}
	if u.latest != nil {
		s.Available = Newer(u.latest.Version, u.Current)
		if s.Blocked == NotBlocked && s.Available && u.latest.assets[u.assetName(u.latest.Tag)] == "" {
			s.Blocked = NoDownload
		}
	}
	return s
}

// begin moves from Idle to st, or says the updater is busy.
func (u *Updater) begin(st State) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.state != Idle {
		return ErrBusy
	}
	u.state = st
	return nil
}

func (u *Updater) end(st State) {
	u.mu.Lock()
	u.state = st
	u.mu.Unlock()
}

// Check asks GitHub for the newest release now.
func (u *Updater) Check(ctx context.Context) (Status, error) {
	if err := u.begin(Checking); err != nil {
		return u.Status(), err
	}
	defer u.end(Idle)
	err := u.check(ctx)
	return u.Status(), err
}

func (u *Updater) check(ctx context.Context) error {
	rel, err := u.fetchLatest(ctx)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.checked = time.Now()
	if err != nil {
		u.checkErr = err.Error()
		return err
	}
	u.latest, u.checkErr = rel, ""
	return nil
}

func (u *Updater) fetchLatest(ctx context.Context) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	api := strings.TrimSuffix(cmpOr(u.API, apiURL), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/repos/"+cmpOr(u.Repo, DefaultRepo)+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "mimir-server/"+cmpOr(u.Current, "dev"))
	res, err := u.client().Do(req)
	if err != nil {
		return nil, errors.New("couldn't reach GitHub to look for a new version")
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return nil, errors.New("no version has been released yet")
	case res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New("GitHub asked to wait before looking again; try again in an hour")
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub answered %s when asked for the newest version", res.Status)
	}
	var body struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxRelease)).Decode(&body); err != nil {
		return nil, fmt.Errorf("GitHub's answer couldn't be read: %w", err)
	}
	if !IsRelease(body.TagName) {
		return nil, fmt.Errorf("the newest release, %q, isn't a version number", body.TagName)
	}
	rel := &Release{Version: strings.TrimPrefix(body.TagName, "v"), Tag: body.TagName, URL: body.HTMLURL, Published: body.PublishedAt, assets: map[string]string{}}
	for _, a := range body.Assets {
		if strings.HasPrefix(a.URL, "https://") {
			rel.assets[a.Name] = a.URL
		}
	}
	return rel, nil
}

// assetName is the release file for this machine: the archive GoReleaser
// makes, or for a static build on Linux (the container image and Alpine
// package) the static one, since the usual Linux build needs glibc.
func (u *Updater) assetName(tag string) string {
	goos, goarch := cmpOr(u.goos, runtime.GOOS), cmpOr(u.goarch, runtime.GOARCH)
	name := fmt.Sprintf("mimir-server_%s_%s_%s", tag, goos, goarch)
	static := isStatic
	if u.static != nil {
		static = u.static
	}
	if goos == "linux" && static() {
		name += "_static"
	}
	if goos == "windows" {
		return name + ".zip"
	}
	return name + ".tar.gz"
}

// Install looks for the newest release, and when it is newer, downloads
// and checks it in the background, then calls Restart. It returns once
// the download has started; Status follows it.
func (u *Updater) Install(ctx context.Context) error {
	if err := u.begin(Installing); err != nil {
		return err
	}
	u.mu.Lock()
	u.installErr = ""
	u.mu.Unlock()
	if err := u.check(ctx); err != nil {
		u.end(Idle)
		return err
	}
	st := u.Status()
	switch {
	case st.Blocked == DevBuild:
		u.end(Idle)
		return errors.New("a development build can't update itself")
	case !st.Available:
		u.end(Idle)
		return ErrUpToDate
	case st.Blocked == NoDownload:
		u.end(Idle)
		return fmt.Errorf("version %s has no download for this machine (%s)", st.Latest.Version, u.assetName(st.Latest.Tag))
	}
	go u.install(st.Latest)
	return nil
}

// InstallNow does what Install does and waits for it, for automatic
// updates.
func (u *Updater) InstallNow(ctx context.Context) error {
	if err := u.Install(ctx); err != nil {
		return err
	}
	for {
		st := u.Status()
		switch {
		case st.State == Restarting:
			return nil
		case st.State == Idle:
			return errors.New(cmpOr(st.InstallErr, "the update stopped"))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (u *Updater) install(rel *Release) {
	// The download goes on even when the request that asked for it ends.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	log := u.log().With("from", u.Current, "to", rel.Version)
	log.Info("update: downloading")
	err := u.stage(ctx, rel)
	if err != nil {
		log.Error("update: failed", "error", err)
		u.mu.Lock()
		u.state, u.installErr = Idle, err.Error()
		u.mu.Unlock()
		return
	}
	log.Info("update: ready; restarting on the new version")
	u.end(Restarting)
	if u.Restart != nil {
		u.Restart()
	}
}

// stage downloads rel, checks it, and keeps it as the version Launch
// switches to.
func (u *Updater) stage(ctx context.Context, rel *Release) error {
	name := u.assetName(rel.Tag)
	sumsURL := rel.assets["SHA256SUMS"]
	if sumsURL == "" {
		return fmt.Errorf("version %s has no SHA256SUMS to check its download against", rel.Version)
	}
	want, err := u.expectedSum(ctx, sumsURL, name)
	if err != nil {
		return err
	}
	dir := Dir(u.DataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("couldn't make the updates folder: %w", err)
	}
	archive, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return fmt.Errorf("couldn't save the download: %w", err)
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	got, err := u.download(ctx, rel.assets[name], archive)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("the download of %s doesn't match its SHA256SUMS, so it wasn't used", name)
	}

	exe := binaryPrefix + rel.Version
	if cmpOr(u.goos, runtime.GOOS) == "windows" {
		exe += ".exe"
	}
	tmp := filepath.Join(dir, exe+".tmp")
	defer os.Remove(tmp)
	if strings.HasSuffix(name, ".zip") {
		err = unzip(archive, tmp)
	} else {
		err = untar(archive, tmp)
	}
	if err != nil {
		return err
	}
	if err := tryRun(ctx, tmp); err != nil {
		return err
	}
	final := filepath.Join(dir, exe)
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("couldn't keep the new version: %w", err)
	}
	sum, err := fileSHA256(final)
	if err != nil {
		return err
	}
	return writeManifest(dir, manifest{Version: rel.Version, File: exe, SHA256: sum, From: strings.TrimPrefix(u.Current, "v")})
}

func (u *Updater) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mimir-server/"+cmpOr(u.Current, "dev"))
	res, err := u.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't download %s", path.Base(url))
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("downloading %s: GitHub answered %s", path.Base(url), res.Status)
	}
	return res, nil
}

// expectedSum reads the SHA256SUMS file for name's checksum.
func (u *Updater) expectedSum(ctx context.Context, url, name string) (string, error) {
	res, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(res.Body, maxSums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == sha256.Size*2 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS doesn't list %s", name)
}

func (u *Updater) download(ctx context.Context, url string, to *os.File) (string, error) {
	res, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(to, h), io.LimitReader(res.Body, maxArchive+1))
	if err != nil {
		return "", fmt.Errorf("the download stopped: %w", err)
	}
	if n > maxArchive {
		return "", errors.New("the download is larger than any release should be")
	}
	if _, err := to.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// isBinary says whether an archive entry is the server itself.
func isBinary(name string) bool {
	b := path.Base(strings.ReplaceAll(name, `\`, "/"))
	return b == "mimir-server" || b == "mimir-server.exe"
}

func untar(f *os.File, to string) error {
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("the download isn't a .tar.gz archive: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("the download has no mimir-server program in it")
		}
		if err != nil {
			return fmt.Errorf("the download couldn't be read: %w", err)
		}
		if h.Typeflag == tar.TypeReg && isBinary(h.Name) {
			return writeBinary(tr, to)
		}
	}
}

func unzip(f *os.File, to string) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return fmt.Errorf("the download isn't a .zip archive: %w", err)
	}
	for _, zf := range zr.File {
		if !zf.FileInfo().IsDir() && isBinary(zf.Name) {
			r, err := zf.Open()
			if err != nil {
				return err
			}
			defer r.Close()
			return writeBinary(r, to)
		}
	}
	return errors.New("the download has no mimir-server program in it")
}

func writeBinary(r io.Reader, to string) error {
	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("couldn't write the new version: %w", err)
	}
	n, err := io.Copy(out, io.LimitReader(r, maxBinary+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("couldn't write the new version: %w", err)
	}
	if n > maxBinary {
		return errors.New("the program in the download is larger than any release should be")
	}
	return nil
}

// tryRun starts the new binary with -help to see that it runs on this
// machine: a build for the wrong C library or CPU fails here, before the
// server stops.
func tryRun(ctx context.Context, bin string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-help")
	cmd.Env = append(os.Environ(), testEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("the new version doesn't run on this machine (%v) %s", err, msg)
	}
	return nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
