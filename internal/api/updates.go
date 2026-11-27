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

package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/update"
)

// KeyUpdates is the settings key for automatic updates.
const KeyUpdates = "updates"

// Updates is the stored update setting.
type Updates struct {
	// Auto installs a newer version on its own when one is out.
	Auto bool `json:"auto"`
}

// LoadUpdates reads the update setting; off until someone turns it on.
func LoadUpdates(ctx context.Context, st *store.Store) (Updates, error) {
	var u Updates
	raw, err := st.GetSetting(ctx, KeyUpdates)
	if err != nil || raw == "" {
		return u, err
	}
	return u, json.Unmarshal([]byte(raw), &u)
}

// LoadUpdatesSetting reads the stored update setting into the service.
// Call it once at start.
func (s *Service) LoadUpdatesSetting(ctx context.Context) error {
	u, err := LoadUpdates(ctx, s.store)
	if err != nil {
		return err
	}
	s.autoUpdate.Store(u.Auto)
	return nil
}

// AutoUpdates says whether automatic updates are on.
func (s *Service) AutoUpdates() bool { return s.Updater != nil && s.autoUpdate.Load() }

func (s *Service) updatesProto() *mimirv1.Updates {
	if s.Updater == nil {
		return &mimirv1.Updates{
			CurrentVersion: about(s.Version, nil).GetVersion(),
			State:          mimirv1.UpdateState_UPDATE_STATE_IDLE,
			Blocked:        mimirv1.UpdateBlock_UPDATE_BLOCK_TURNED_OFF,
		}
	}
	st := s.Updater.Status()
	res := &mimirv1.Updates{
		CurrentVersion: cmpOr(st.Current, "dev"),
		Available:      st.Available,
		CheckError:     sentence(st.CheckErr),
		Auto:           s.autoUpdate.Load(),
		InstallError:   sentence(st.InstallErr),
	}
	if st.Latest != nil {
		res.LatestVersion = st.Latest.Version
		res.ReleaseUrl = st.Latest.URL
		if !st.Latest.Published.IsZero() {
			res.Published = timestamppb.New(st.Latest.Published)
		}
	}
	if !st.Checked.IsZero() {
		res.Checked = timestamppb.New(st.Checked)
	}
	switch st.State {
	case update.Checking:
		res.State = mimirv1.UpdateState_UPDATE_STATE_CHECKING
	case update.Installing:
		res.State = mimirv1.UpdateState_UPDATE_STATE_INSTALLING
	case update.Restarting:
		res.State = mimirv1.UpdateState_UPDATE_STATE_RESTARTING
	default:
		res.State = mimirv1.UpdateState_UPDATE_STATE_IDLE
	}
	switch st.Blocked {
	case update.DevBuild:
		res.Blocked = mimirv1.UpdateBlock_UPDATE_BLOCK_DEV_BUILD
	case update.NoDownload:
		res.Blocked = mimirv1.UpdateBlock_UPDATE_BLOCK_NO_DOWNLOAD
	}
	return res
}

// GetUpdates says what the last check found.
func (s *Service) GetUpdates(_ context.Context, _ *connect.Request[mimirv1.GetUpdatesRequest]) (*connect.Response[mimirv1.GetUpdatesResponse], error) {
	return connect.NewResponse(&mimirv1.GetUpdatesResponse{Updates: s.updatesProto()}), nil
}

var errUpdatesOff = connect.NewError(connect.CodeFailedPrecondition, errors.New("Updates are turned off on this server (-updates=false)."))

// CheckForUpdates asks GitHub for the newest release now. A failed check
// comes back in check_error, so the page can say what went wrong.
func (s *Service) CheckForUpdates(ctx context.Context, _ *connect.Request[mimirv1.CheckForUpdatesRequest]) (*connect.Response[mimirv1.CheckForUpdatesResponse], error) {
	if s.Updater == nil {
		return nil, errUpdatesOff
	}
	if _, err := s.Updater.Check(ctx); err != nil && errors.Is(err, update.ErrBusy) {
		return nil, connect.NewError(connect.CodeAborted, errors.New("An update is already under way."))
	}
	return connect.NewResponse(&mimirv1.CheckForUpdatesResponse{Updates: s.updatesProto()}), nil
}

// InstallUpdate starts installing the newest release.
func (s *Service) InstallUpdate(ctx context.Context, _ *connect.Request[mimirv1.InstallUpdateRequest]) (*connect.Response[mimirv1.InstallUpdateResponse], error) {
	if s.Updater == nil {
		return nil, errUpdatesOff
	}
	p, _ := auth.From(ctx)
	if err := s.Updater.Install(ctx); err != nil {
		code := connect.CodeFailedPrecondition
		if errors.Is(err, update.ErrBusy) {
			code = connect.CodeAborted
		}
		return nil, connect.NewError(code, errors.New(sentence(err.Error())))
	}
	st := s.Updater.Status()
	if st.Latest != nil {
		s.audit(ctx, p.Actor(), "settings.update", st.Latest.Version, "installing version "+st.Latest.Version+" over "+st.Current)
	}
	return connect.NewResponse(&mimirv1.InstallUpdateResponse{Updates: s.updatesProto()}), nil
}

// UpdateAutoUpdates turns automatic updates on or off.
func (s *Service) UpdateAutoUpdates(ctx context.Context, req *connect.Request[mimirv1.UpdateAutoUpdatesRequest]) (*connect.Response[mimirv1.UpdateAutoUpdatesResponse], error) {
	if s.Updater == nil {
		return nil, errUpdatesOff
	}
	p, _ := auth.From(ctx)
	u := Updates{Auto: req.Msg.GetAuto()}
	b, err := json.Marshal(u)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if err := s.store.SetSetting(ctx, KeyUpdates, string(b)); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.autoUpdate.Store(u.Auto)
	what := "off: new versions are installed only when someone presses Update"
	if u.Auto {
		what = "on: new versions are installed as they come out"
		s.kickUpdates()
	}
	s.audit(ctx, p.Actor(), "settings.auto_updates", "", what)
	return connect.NewResponse(&mimirv1.UpdateAutoUpdatesResponse{Updates: s.updatesProto()}), nil
}

// kickUpdates asks the automatic updater to look now.
func (s *Service) kickUpdates() {
	select {
	case s.updateKick <- struct{}{}:
	default:
	}
}

// RunUpdates installs new versions as they come out while automatic
// updates are on: it looks soon after start and when they are turned on,
// then every interval.
func (s *Service) RunUpdates(ctx context.Context, interval time.Duration) {
	if s.Updater == nil {
		return
	}
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.updateKick:
		}
		t.Reset(interval)
		if !s.AutoUpdates() {
			continue
		}
		if _, err := s.Updater.Check(ctx); err != nil {
			if !errors.Is(err, update.ErrBusy) {
				s.log.Warn("update: check failed", "error", err)
			}
			continue
		}
		st := s.Updater.Status()
		if !st.Available || st.Blocked != update.NotBlocked {
			continue
		}
		s.auditIn(ctx, "", "automatic updates", "settings.update", st.Latest.Version, "installing version "+st.Latest.Version+" over "+st.Current)
		err := s.Updater.InstallNow(ctx)
		switch {
		case err == nil:
			return // the server is restarting
		case ctx.Err() != nil, errors.Is(err, update.ErrBusy), errors.Is(err, update.ErrUpToDate):
		default:
			s.log.Error("update: automatic update failed", "version", st.Latest.Version, "error", err)
			if s.Notifier != nil {
				s.Notifier.Notify(ctx, "Automatic update to mimir-server "+st.Latest.Version+" failed",
					"mimir-server "+st.Current+" couldn't update itself to "+st.Latest.Version+": "+sentence(err.Error())+"\n\nIt keeps running the version it has and tries again later. You can also try now from the About page.")
			}
		}
	}
}

// Updated records, once the server serves after an update, which version
// it came from.
func (s *Service) Updated(ctx context.Context, from string) {
	cur := about(s.Version, nil).GetVersion()
	s.auditIn(ctx, "", "mimir-server", "settings.updated", cur, "now running version "+cur+", updated from "+from)
}

// sentence makes an updater message read as a sentence: a capital first
// and a full stop last.
func sentence(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	r, n := utf8.DecodeRuneInString(msg)
	msg = string(unicode.ToUpper(r)) + msg[n:]
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
