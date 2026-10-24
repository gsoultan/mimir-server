package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gsoultan/mimir-server/internal/testdb"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), testdb.URL(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenNeedsURL(t *testing.T) {
	if _, err := Open(context.Background(), "", t.TempDir()); err == nil {
		t.Fatal("Open with no database URL succeeded")
	}
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	weights := []byte("weights")
	m, err := s.Create(ctx, Model{Name: "digits", Architecture: "mnist-mlp", Labels: []string{"0", "1"}, InputShape: []int{784}}, weights)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidID(m.ID) || m.SizeBytes != int64(len(weights)) {
		t.Fatalf("Create = %+v", m)
	}

	got, err := s.Get(ctx, m.ID)
	if err != nil || got.Name != "digits" || got.TeamID != DefaultTeam || len(got.Labels) != 2 || got.InputShape[0] != 784 || !got.Created.Equal(m.Created) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	b, err := s.Weights(m.ID)
	if err != nil || !bytes.Equal(b, weights) {
		t.Fatalf("Weights = %q, %v", b, err)
	}

	if _, err := s.Update(ctx, m.ID, "digits v2", "better"); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, DefaultTeam)
	if err != nil || len(list) != 1 || list[0].Name != "digits v2" || list[0].Description != "better" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if _, err := s.Update(ctx, "01900000-0000-7000-8000-00000000ffff", "x", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update missing: %v", err)
	}

	if err := s.Delete(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete: %v", err)
	}
	if _, err := s.Weights(m.ID); err == nil {
		t.Fatal("weights file survived delete")
	}
	if err := s.Delete(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete: %v", err)
	}
}

// A second Open of the same database finds the schema current and applies
// nothing, and the data is still there.
func TestReopenKeepsData(t *testing.T) {
	ctx := context.Background()
	url, dir := testdb.URL(t), t.TempDir()
	s, err := Open(ctx, url, dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Applied() == 0 {
		t.Fatal("first Open applied no migration steps")
	}
	m, err := s.Create(ctx, Model{Name: "a", Architecture: "mnist-mlp"}, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(ctx, url, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Applied() != 0 {
		t.Fatalf("second Open applied %d steps", s.Applied())
	}
	if _, err := s.Get(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	teams, err := s.ListTeams(ctx)
	if err != nil || len(teams) != 1 || teams[0].ID != DefaultTeam || teams[0].Models != 1 {
		t.Fatalf("teams = %+v, %v", teams, err)
	}
}

func TestUsersSessionsAndKeys(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	m, err := s.Create(ctx, Model{Name: "m", Architecture: "mnist-mlp"}, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(ctx, User{Email: "a@example.com", Name: "A", PasswordHash: "h", Access: Access{Role: Viewer, ModelIDs: []string{m.ID, m.ID}}})
	if err != nil || len(u.Access.ModelIDs) != 1 || u.Access.ModelIDs[0] != m.ID || u.TeamID != DefaultTeam {
		t.Fatalf("CreateUser = %+v, %v", u, err)
	}
	if _, err := s.CreateUser(ctx, User{Email: "a@example.com", Name: "B", PasswordHash: "h", Access: Access{Role: Viewer}}); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate email: %v", err)
	}
	if n, err := s.CountUsers(ctx); err != nil || n != 1 {
		t.Fatalf("CountUsers = %d, %v", n, err)
	}

	keep, err := s.CreateSession(ctx, "t1", Session{UserID: u.ID, Device: "firefox", Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession(ctx, "t2", Session{UserID: u.ID, Device: "chrome", Expires: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	se, who, err := s.LookupSession(ctx, "t1", "firefox")
	if err != nil || se.ID != keep.ID || who.ID != u.ID || who.LastSignIn.IsZero() {
		t.Fatalf("LookupSession = %+v %+v %v", se, who, err)
	}
	if _, _, err := s.LookupSession(ctx, "t1", "safari"); !errors.Is(err, ErrWrongDevice) {
		t.Fatalf("other browser: %v", err)
	}
	if n, err := s.RevokeOtherSessions(ctx, u.ID, keep.ID); err != nil || n != 1 {
		t.Fatalf("RevokeOtherSessions = %d, %v", n, err)
	}
	if list, err := s.ListSessions(ctx, u.ID); err != nil || len(list) != 1 {
		t.Fatalf("ListSessions = %+v, %v", list, err)
	}

	// A TOTP step is accepted once.
	if ok, err := s.UseTOTPStep(ctx, u.ID, 5); err != nil || !ok {
		t.Fatalf("first step: %v %v", ok, err)
	}
	if ok, err := s.UseTOTPStep(ctx, u.ID, 5); err != nil || ok {
		t.Fatalf("replayed step: %v %v", ok, err)
	}
	if err := s.SetPendingTOTP(ctx, u.ID, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableTOTP(ctx, u.ID, keep.ID, []string{"c1", "c2"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetUser(ctx, u.ID); got.TOTPSecret != "secret" || got.TOTPPending != "" || got.TOTPLastStep != 0 {
		t.Fatalf("after EnableTOTP: %+v", got)
	}
	if used, left, err := s.UseRecoveryCode(ctx, u.ID, "c1"); err != nil || !used || left != 1 {
		t.Fatalf("UseRecoveryCode = %v %d %v", used, left, err)
	}
	if used, _, err := s.UseRecoveryCode(ctx, u.ID, "c1"); err != nil || used {
		t.Fatalf("spent code again: %v %v", used, err)
	}

	k, err := s.CreateAPIKey(ctx, APIKey{UserID: u.ID, Name: "app", Prefix: "mk_", Access: Access{Role: Viewer, ModelIDs: []string{m.ID}}}, "secret-hash")
	if err != nil {
		t.Fatal(err)
	}
	gotKey, owner, err := s.KeyUser(ctx, "secret-hash")
	if err != nil || gotKey.ID != k.ID || owner.ID != u.ID || !gotKey.Access.Allows(m.ID) {
		t.Fatalf("KeyUser = %+v %+v %v", gotKey, owner, err)
	}
	// Its first use is recorded; uses within the minute after don't write.
	used, _ := s.ListAPIKeys(ctx, u.ID)
	if len(used) != 1 || used[0].LastUsed.IsZero() {
		t.Fatalf("after first use: %+v", used)
	}
	if again, _, err := s.KeyUser(ctx, "secret-hash"); err != nil || !again.LastUsed.Equal(used[0].LastUsed) {
		t.Fatalf("second use = %+v, %v; first recorded %v", again.LastUsed, err, used[0].LastUsed)
	}
	if after, _ := s.ListAPIKeys(ctx, u.ID); !after[0].LastUsed.Equal(used[0].LastUsed) {
		t.Fatalf("last used moved from %v to %v within a minute", used[0].LastUsed, after[0].LastUsed)
	}
	if err := s.DeleteAPIKey(ctx, "someone-else", k.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete another's key: %v", err)
	}

	// Moving teams drops grants, keys and other sessions.
	tm, err := s.CreateTeam(ctx, "Other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTeam(ctx, "Other"); !errors.Is(err, ErrTeamNameTaken) {
		t.Fatalf("duplicate team: %v", err)
	}
	if err := s.MoveUser(ctx, u.ID, tm.ID, keep.ID); err != nil {
		t.Fatal(err)
	}
	moved, _ := s.GetUser(ctx, u.ID)
	keys, _ := s.ListAPIKeys(ctx, u.ID)
	if moved.TeamID != tm.ID || len(moved.Access.ModelIDs) != 0 || len(keys) != 0 {
		t.Fatalf("after MoveUser: %+v, keys %d", moved, len(keys))
	}
	if err := s.DeleteTeam(ctx, tm.ID); !errors.Is(err, ErrTeamInUse) {
		t.Fatalf("DeleteTeam in use: %v", err)
	}
	if _, err := s.CreateAPIKey(ctx, APIKey{UserID: u.ID, Name: "app2", Prefix: "mk_", Access: Access{Role: Viewer}}, "secret-hash-2"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword(ctx, u.ID, "h2", false); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListSessions(ctx, u.ID); len(list) != 0 {
		t.Fatalf("sessions after SetPassword: %d", len(list))
	}
	if keys, _ := s.ListAPIKeys(ctx, u.ID); len(keys) != 1 {
		t.Fatalf("a routine SetPassword left %d keys, want 1", len(keys))
	}
	if err := s.SetPassword(ctx, u.ID, "h3", true); err != nil {
		t.Fatal(err)
	}
	if keys, _ := s.ListAPIKeys(ctx, u.ID); len(keys) != 0 {
		t.Fatalf("SetPassword revoking keys left %d", len(keys))
	}

	if err := s.Audit(ctx, AuditEvent{Actor: "a", Action: "x", TeamID: tm.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.Audit(ctx, AuditEvent{Actor: "a", Action: "y"}); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListAuditEvents(ctx, "", "", 10)
	if err != nil || len(events) != 2 || events[0].Action != "y" || events[1].TeamName != "Other" {
		t.Fatalf("ListAuditEvents = %+v, %v", events, err)
	}
	if older, _ := s.ListAuditEvents(ctx, "", events[0].ID, 10); len(older) != 1 || older[0].Action != "x" {
		t.Fatalf("older events = %+v", older)
	}
}

func TestJobsAndUsage(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	d, err := s.CreateDataset(ctx, Dataset{Name: "d", Kind: DatasetTable, Rows: 3, Features: []string{"a"}}, []byte("a,b\n1,2\n"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateJob(ctx, Job{Name: "a", Architecture: "table-mlp", DatasetID: d.ID, CreatedBy: "ann@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateJob(ctx, Job{Name: "b", Architecture: "table-mlp", DatasetID: d.ID, CreatedBy: "app (ann@example.com)"})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.ActiveJobs(ctx, "ann@example.com"); err != nil || n != 2 {
		t.Fatalf("ActiveJobs = %d, %v", n, err)
	}
	if err := s.DeleteDataset(ctx, d.ID); !errors.Is(err, ErrDatasetInUse) {
		t.Fatalf("DeleteDataset in use: %v", err)
	}
	got, err := s.ClaimJob(ctx, a.ID)
	if err != nil || got.ID != b.ID || got.Status != JobRunning {
		t.Fatalf("ClaimJob = %+v, %v", got, err)
	}
	if err := s.DeleteJob(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete running job: %v", err)
	}
	if err := s.SetJobStatus(ctx, b.ID, JobFailed, "boom", ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.RequeueJob(ctx, b.ID); err != nil || !ok {
		t.Fatalf("RequeueJob = %v, %v", ok, err)
	}
	if ok, err := s.RequeueJob(ctx, b.ID); err != nil || ok {
		t.Fatalf("RequeueJob queued job = %v, %v", ok, err)
	}
	if err := s.JobProgress(ctx, a.ID, 1, 2, 3, []Epoch{{Epoch: 1, Loss: 0.5}}); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.GetJob(ctx, a.ID); j.Epoch != 1 || j.StepsPerEpoch != 3 || len(j.History) != 1 {
		t.Fatalf("after JobProgress: %+v", j)
	}
	if _, err := s.ClaimJob(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetRunningJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.JobStatusOf(ctx, a.ID); st != JobQueued {
		t.Fatalf("after reset: %v", st)
	}

	u, err := s.CreateUser(ctx, User{Email: "ann@example.com", Name: "Ann", PasswordHash: "h", Access: Access{Role: Editor, AllModels: true}})
	if err != nil {
		t.Fatal(err)
	}
	k := UsageKey{Month: "2026-10", UserID: u.ID}
	for range 2 {
		if err := s.AddUsage(ctx, map[UsageKey]int64{k: 3, {Month: "2026-10", UserID: u.ID, KeyID: "k", KeyName: "app"}: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.UserUsage(ctx, "2026-10", u.ID); err != nil || n != 8 {
		t.Fatalf("UserUsage = %d, %v", n, err)
	}
	list, err := s.ListUsage(ctx, "2026-10", "", DefaultTeam)
	if err != nil || len(list) != 2 || list[0].Predictions != 6 || list[0].Email != "ann@example.com" {
		t.Fatalf("ListUsage = %+v, %v", list, err)
	}
}

func TestWorkersAndHousekeeping(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	w, err := s.CreateWorker(ctx, Worker{Name: "gpu", CreatedBy: "a"}, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SetWorkerKey(ctx, w.ID, []byte("k1")); err != nil || !ok {
		t.Fatalf("SetWorkerKey = %v, %v", ok, err)
	}
	if ok, err := s.SetWorkerKey(ctx, w.ID, []byte("k2")); err != nil || ok {
		t.Fatalf("second SetWorkerKey = %v, %v", ok, err)
	}
	if err := s.ReplaceWorkerToken(ctx, w.ID, "tok2"); err != nil {
		t.Fatal(err)
	}
	got, err := s.WorkerByToken(ctx, "tok2")
	if err != nil || got.PublicKey != nil {
		t.Fatalf("WorkerByToken = %+v, %v", got, err)
	}
	start := time.Now().Add(-10 * time.Minute)
	for i := range 10 {
		if err := s.AddWorkerSample(ctx, w.ID, WorkerSample{Time: start.Add(time.Duration(i) * time.Minute), CPUPercent: float64(i), Busy: 1, Capacity: 4}); err != nil {
			t.Fatal(err)
		}
	}
	samples, err := s.WorkerSamples(ctx, w.ID, start, 5)
	if err != nil || len(samples) == 0 || len(samples) > 6 || samples[0].Capacity != 4 {
		t.Fatalf("WorkerSamples = %+v, %v", samples, err)
	}
	m, err := s.Create(ctx, Model{Name: "m", Architecture: "mnist-mlp"}, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetModelWorker(ctx, m.ID, w.ID); err != nil {
		t.Fatal(err)
	}
	placed, _, err := s.Placement(ctx)
	if err != nil || placed[w.ID] != 1 {
		t.Fatalf("Placement = %v, %v", placed, err)
	}
	if err := s.DeleteWorker(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Get(ctx, m.ID); m.WorkerID != "" {
		t.Fatalf("model still placed on a deleted worker: %+v", m)
	}

	if err := s.SetSetting(ctx, "k", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "k", "2"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.GetSetting(ctx, "k"); err != nil || v != "2" {
		t.Fatalf("GetSetting = %q, %v", v, err)
	}
	if err := s.Audit(ctx, AuditEvent{Actor: "a", Action: "old", Time: time.Now().Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Trim(ctx, 24*time.Hour, 24*time.Hour); err != nil || n != 1 {
		t.Fatalf("Trim = %d, %v", n, err)
	}
	st, err := s.Stats(ctx)
	if err != nil || st.SizeBytes <= 0 || st.Models != 1 || st.SchemaVersion != LatestSchemaVersion() || st.SchemaVersion < 1 {
		t.Fatalf("Stats = %+v, %v", st, err)
	}
}

func TestBackup(t *testing.T) {
	s := open(t)
	path := filepath.Join(t.TempDir(), "mimir.dump")
	err := s.Backup(context.Background(), path)
	if _, lookErr := exec.LookPath("pg_dump"); lookErr != nil {
		if !errors.Is(err, ErrBackupUnavailable) {
			t.Fatalf("Backup without pg_dump: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(b, []byte("PGDMP")) {
		t.Fatalf("dump starts %q, %v", b[:min(len(b), 8)], err)
	}
}

func TestLibpqURL(t *testing.T) {
	conn, pw, err := libpqURL("postgres://mimir:s3cret@db:5432/mimir?sslmode=require&pool_max_conns=4")
	if err != nil || pw != "s3cret" || conn != "postgres://mimir@db:5432/mimir?sslmode=require" {
		t.Fatalf("libpqURL = %q %q %v", conn, pw, err)
	}
	if got := Redact("postgres://mimir:s3cret@db:5432/mimir?sslmode=require"); got != "postgres://mimir:xxxxx@db:5432/mimir" {
		t.Fatalf("Redact = %q", got)
	}
}

func TestRefuseOwnDatabase(t *testing.T) {
	s := open(t)
	ours := s.pool.Config().ConnConfig
	port := strconv.Itoa(int(ours.Port))
	for _, tc := range []struct {
		network, address, database string
		refused                    bool
	}{
		{"tcp4", "127.0.0.1:" + port, ours.Database, true},
		{"tcp4", "127.0.0.2:" + port, ours.Database, true},
		{"tcp6", "[::1]:" + port, ours.Database, true},
		{"unix", "/var/run/postgresql/.s.PGSQL." + port, ours.Database, true},
		{"tcp4", "127.0.0.1:" + port, "shop", false},          // another database on the same server
		{"tcp4", "127.0.0.1:1", ours.Database, false},         // another server on this machine
		{"tcp4", "203.0.113.9:" + port, ours.Database, false}, // another machine
	} {
		err := s.RefuseOwnDatabase(tc.network, tc.address, tc.database)
		if refused := errors.Is(err, ErrOwnDatabase); refused != tc.refused {
			t.Errorf("%s %s %s: refused = %v", tc.network, tc.address, tc.database, refused)
		}
	}
}

// TestClaimDueJobs: a due keep-learning job is handed out once, then not
// again until its lease runs out.
func TestClaimDueJobs(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	d, err := s.CreateDataset(ctx, Dataset{Name: "d", Kind: DatasetTable, Rows: 3, Features: []string{"a"}}, []byte("a,b\n1,2\n"))
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(ctx, Job{Name: "a", Architecture: "table-mlp", DatasetID: d.ID, RepeatSeconds: 3600, MinNewRows: 5, TrainedRows: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetJobStatus(ctx, j.ID, JobSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(2 * time.Hour)
	if got, err := s.ClaimDueJobs(ctx, at, time.Hour, 10); err != nil || len(got) != 1 || got[0].MinNewRows != 5 || got[0].TrainedRows != 3 {
		t.Fatalf("first claim = %v, %v", got, err)
	}
	if got, err := s.ClaimDueJobs(ctx, at, time.Hour, 10); err != nil || len(got) != 0 {
		t.Fatalf("second claim = %v, %v", got, err)
	}
	if got, err := s.ClaimDueJobs(ctx, at.Add(61*time.Minute), time.Hour, 10); err != nil || len(got) != 1 {
		t.Fatalf("claim after the lease = %v, %v", got, err)
	}
	next := time.Now().Add(5 * time.Hour)
	if err := s.SetJobCheck(ctx, j.ID, "1 new row", next); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetJob(ctx, j.ID)
	if err != nil || got.CheckNote != "1 new row" || got.CheckTime.IsZero() || !got.NextRun.Equal(next.Truncate(time.Microsecond)) {
		t.Fatalf("after check = %+v, %v", got, err)
	}
}
