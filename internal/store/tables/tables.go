// Package tables is the schema of the server's own PostgreSQL database,
// declared as storm models: one plain struct per table, with a Schema method
// for what the type cannot say.
//
// storm generates the typed query packages under internal/store/db from
// these structs (`storm generate internal/store/db`), `storm diff` writes the
// reviewable migrations under internal/store/migrations, and the server
// applies the model at startup with migrate.Auto. After changing a struct or
// a Schema method, run all three steps; CI fails with `storm verify -stale`
// or `storm verify -pending` when one was forgotten.
//
// Ids are UUIDv7 strings made by the server and kept as text. Times are
// timestamptz. Small lists (features, labels, columns, webhook events) are
// JSON in text columns, written and read by package store.
package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// Team keeps a group of people and their things apart from other teams.
type Team struct {
	ID         string
	Name       string
	CreateTime time.Time
}

func (t *Team) Schema(s *storm.Table) {
	s.Col(&t.Name).Unique()
	s.Check(storm.RawSQL(`length(name) BETWEEN 1 AND 100`))
}

// User is an account. Role: 1 viewer, 2 editor, 3 admin, 4 owner.
type User struct {
	ID           string
	Email        string
	Name         string
	PasswordHash string
	Role         int32
	AllModels    bool
	Disabled     bool
	CreateTime   time.Time
	LastSignIn   *time.Time
	// TOTPSecret is the confirmed authenticator key; TOTPPending one shown
	// but not yet confirmed. TOTPLastStep refuses a code used before.
	TOTPSecret   string
	TOTPPending  string
	TOTPLastStep int64
	Team         *Team
}

// Aggregates: how many people each team has.
func (u *User) Aggregates(a *storm.Aggregates) {
	perTeam := a.Named("PerTeam")
	perTeam.By(&u.Team)
	perTeam.Count("N")
}

func (u *User) Schema(t *storm.Table) {
	t.Col(&u.Email).Unique()
	t.Col(&u.Disabled).Default("false")
	t.Col(&u.TOTPSecret).Named("totp_secret").Default("''")
	t.Col(&u.TOTPPending).Named("totp_pending").Default("''")
	t.Col(&u.TOTPLastStep).Named("totp_last_step").Default("0")
	t.Check(storm.RawSQL(`length(email) BETWEEN 3 AND 254`))
	t.Check(storm.RawSQL(`length(name) BETWEEN 1 AND 100`))
	t.Check(storm.RawSQL(`password_hash <> ''`))
	t.Check(storm.RawSQL(`role BETWEEN 1 AND 4`))
}

// Model is a stored model's metadata; its weights live on disk.
type Model struct {
	ID           string
	Name         string
	Description  string
	Architecture string
	SizeBytes    int64
	SHA256       string
	CreateTime   time.Time
	UpdateTime   time.Time
	InputShape   string
	Labels       string
	Features     string
	JobID        string
	Accuracy     float64
	Team         *Team
	WorkerID     string
	Columns      string
}

// Aggregates: how many models each team has, and how many are placed on
// each worker.
func (m *Model) Aggregates(a *storm.Aggregates) {
	perTeam := a.Named("PerTeam")
	perTeam.By(&m.Team)
	perTeam.Count("N")

	perWorker := a.Named("PerWorker")
	perWorker.By(&m.WorkerID)
	perWorker.Count("N")
}

func (m *Model) Schema(t *storm.Table) {
	t.Col(&m.Description).Default("''")
	t.Col(&m.InputShape).Default("''")
	t.Col(&m.Labels).Default("''")
	t.Col(&m.Features).Default("''")
	t.Col(&m.JobID).Default("''")
	t.Col(&m.Accuracy).Default("0")
	t.Col(&m.WorkerID).Default("''")
	t.Col(&m.Columns).Default("'[]'")
	t.Col(&m.Team).NoIndex() // covered by (team_id, create_time DESC)
	t.Index(storm.Desc(&m.CreateTime))
	t.Index(&m.Team, storm.Desc(&m.CreateTime))
	t.Index(&m.WorkerID)
	t.Check(storm.RawSQL(`length(name) BETWEEN 1 AND 100`))
	t.Check(storm.RawSQL(`length(description) <= 500`))
	t.Check(storm.RawSQL(`architecture <> ''`))
	t.Check(storm.RawSQL(`size_bytes > 0`))
	t.Check(storm.RawSQL(`length(sha256) = 64`))
}

// UserModel grants a user one model when the user's AllModels is false.
type UserModel struct {
	User  User
	Model Model
}

func (g *UserModel) Schema(t *storm.Table) {
	t.PrimaryKey(&g.User, &g.Model)
	t.Col(&g.User).OnDelete(storm.Cascade).NoIndex() // the key's first column
	t.Col(&g.Model).OnDelete(storm.Cascade)
}

// Session is one signed-in browser. Only the token's hash is kept.
type Session struct {
	ID         string
	TokenHash  string
	User       User
	AgentHash  string
	Device     string
	IP         string
	Pending    bool
	CreateTime time.Time
	LastSeen   time.Time
	ExpireTime time.Time
}

func (s *Session) Schema(t *storm.Table) {
	t.Col(&s.TokenHash).Unique()
	t.Col(&s.User).OnDelete(storm.Cascade)
	t.Col(&s.Device).Default("''")
	t.Col(&s.IP).Default("''")
	t.Col(&s.Pending).Default("false")
	t.Index(&s.ExpireTime)
}

// RecoveryCode is a hashed one-time code for when the phone is gone.
type RecoveryCode struct {
	User     User
	CodeHash string
}

func (r *RecoveryCode) Schema(t *storm.Table) {
	t.PrimaryKey(&r.User, &r.CodeHash)
	t.Col(&r.User).OnDelete(storm.Cascade).NoIndex()
}

// APIKey is a credential for apps, owned by a user.
type APIKey struct {
	ID         string
	User       User
	Name       string
	Prefix     string
	SecretHash string
	Role       int32
	AllModels  bool
	CreateTime time.Time
	LastUsed   *time.Time
}

func (k *APIKey) Schema(t *storm.Table) {
	t.Col(&k.User).OnDelete(storm.Cascade)
	t.Col(&k.SecretHash).Unique()
	t.Check(storm.RawSQL(`length(name) BETWEEN 1 AND 100`))
	t.Check(storm.RawSQL(`role BETWEEN 1 AND 4`))
}

// APIKeyModel grants a key one model when the key's AllModels is false.
type APIKeyModel struct {
	Key   APIKey
	Model Model
}

func (g *APIKeyModel) Schema(t *storm.Table) {
	t.PrimaryKey(&g.Key, &g.Model)
	t.Col(&g.Key).OnDelete(storm.Cascade).NoIndex()
	t.Col(&g.Model).OnDelete(storm.Cascade)
}

// AuditEvent is one line of who did what. Rows are never updated.
type AuditEvent struct {
	ID     string
	Time   time.Time
	Actor  string
	Action string
	Target string
	Detail string
	Team   *Team
}

func (e *AuditEvent) Schema(t *storm.Table) {
	t.Col(&e.Target).Default("''")
	t.Col(&e.Detail).Default("''")
	t.Col(&e.Team).OnDelete(storm.SetNull).NoIndex()
	t.Index(storm.Desc(&e.Time), storm.Desc(&e.ID))
	t.Index(&e.Team, storm.Desc(&e.Time), storm.Desc(&e.ID))
	t.Index(&e.Target)
}

// Webhook is an endpoint that gets told about events. Events is a JSON
// array of event types; [] means every event.
type Webhook struct {
	ID         string
	Name       string
	URL        string
	Secret     string
	Events     string
	Enabled    bool
	CreateTime time.Time
	Team       *Team
}

func (w *Webhook) Schema(t *storm.Table) {
	t.Col(&w.Events).Default("'[]'")
	t.Col(&w.Enabled).Default("true")
	t.Col(&w.Team).OnDelete(storm.Cascade)
	t.Check(storm.RawSQL(`length(name) BETWEEN 1 AND 100`))
	t.Check(storm.RawSQL(`length(url) BETWEEN 1 AND 2048`))
	t.Check(storm.RawSQL(`secret <> ''`))
}

// Delivery is one event on its way to one webhook: an outbox row while
// pending, history afterwards. Status: 1 pending, 2 delivered, 3 failed.
type Delivery struct {
	ID           string
	Webhook      Webhook
	EventID      string
	EventType    string
	Payload      string
	Status       int32
	Attempts     int64
	ResponseCode int64
	Error        string
	CreateTime   time.Time
	LastAttempt  *time.Time
	NextAttempt  *time.Time
}

func (d *Delivery) Schema(t *storm.Table) {
	t.Col(&d.Webhook).OnDelete(storm.Cascade).NoIndex() // covered by (webhook_id, id DESC)
	t.Col(&d.Attempts).Default("0")
	t.Col(&d.ResponseCode).Default("0")
	t.Col(&d.Error).Default("''")
	t.Index(&d.NextAttempt).Where(storm.RawSQL(`status = 1`))
	t.Index(&d.Webhook, storm.Desc(&d.ID))
	t.Index(&d.CreateTime)
	t.Check(storm.RawSQL(`status BETWEEN 1 AND 3`))
}

// Setting is a small key/value pair, such as alert and retention choices.
type Setting struct {
	Key   string
	Value string
}

func (s *Setting) Schema(t *storm.Table) { t.PrimaryKey(&s.Key) }

// Dataset is a set of examples. Kind: 1 table (CSV), 2 mnist, 3 photo
// folders (ZIP), 4 table and photos (ZIP). Features, labels, input shape
// and columns are JSON. DSN is a secret and never sent to clients.
type Dataset struct {
	ID           string
	Name         string
	Kind         int32
	Rows         int64
	Features     string
	Labels       string
	InputShape   string
	Target       string
	SizeBytes    int64
	CreateTime   time.Time
	Driver       string
	DSN          string
	SourceQuery  string // not "query": storm derives Go names from column names, and Query is taken
	RefreshTime  *time.Time
	Team         *Team
	Columns      string
	NumberTarget bool
	Skipped      int64
}

// Aggregates: how many datasets each team has.
func (d *Dataset) Aggregates(a *storm.Aggregates) {
	perTeam := a.Named("PerTeam")
	perTeam.By(&d.Team)
	perTeam.Count("N")
}

func (d *Dataset) Schema(t *storm.Table) {
	t.Col(&d.Features).Default("'[]'")
	t.Col(&d.Labels).Default("'[]'")
	t.Col(&d.InputShape).Default("'[]'")
	t.Col(&d.Target).Default("''")
	t.Col(&d.SizeBytes).Default("0")
	t.Col(&d.Driver).Default("''")
	t.Col(&d.DSN).Named("dsn").Default("''")
	t.Col(&d.SourceQuery).Default("''")
	t.Col(&d.Columns).Default("'[]'")
	t.Col(&d.NumberTarget).Default("false")
	t.Col(&d.Skipped).Default("0")
	t.Col(&d.Team).NoIndex() // covered by (team_id, create_time DESC)
	t.Index(&d.Team, storm.Desc(&d.CreateTime))
	t.Check(storm.RawSQL(`length(name) BETWEEN 1 AND 100`))
	t.Check(storm.RawSQL(`kind IN (1, 2, 3, 4)`))
}

// TrainingJob is one training run. Status: 1 queued, 2 running,
// 3 succeeded, 4 failed, 5 cancelled. History is a JSON array of epochs.
type TrainingJob struct {
	ID            string
	Name          string
	Description   string
	Architecture  string
	Dataset       Dataset
	Epochs        int64
	BatchSize     int64
	LearningRate  float64
	HoldOut       float64
	Status        int32
	Error         string
	Epoch         int64
	Step          int64
	StepsPerEpoch int64
	History       string
	ModelID       string
	Worker        string
	CreatedBy     string
	CreateTime    time.Time
	StartTime     *time.Time
	EndTime       *time.Time
	RepeatSeconds int64
	NextRun       *time.Time
	ParentID      string
	BaseModelID   string
	Note          string
	Team          *Team
	RunOn         string
}

// Aggregates: how many runs wait for each worker.
func (j *TrainingJob) Aggregates(a *storm.Aggregates) {
	perRunOn := a.Named("PerRunOn")
	perRunOn.By(&j.RunOn)
	perRunOn.Count("N")
}

func (j *TrainingJob) Schema(t *storm.Table) {
	t.Col(&j.Dataset).OnDelete(storm.Restrict)
	t.Col(&j.Description).Default("''")
	t.Col(&j.Error).Default("''")
	t.Col(&j.Epoch).Default("0")
	t.Col(&j.Step).Default("0")
	t.Col(&j.StepsPerEpoch).Default("0")
	t.Col(&j.History).Default("'[]'")
	t.Col(&j.ModelID).Default("''")
	t.Col(&j.Worker).Default("''")
	t.Col(&j.CreatedBy).Default("''")
	t.Col(&j.RepeatSeconds).Default("0")
	t.Col(&j.ParentID).Default("''")
	t.Col(&j.BaseModelID).Default("''")
	t.Col(&j.Note).Default("''")
	t.Col(&j.RunOn).Default("''")
	t.Col(&j.Team).NoIndex() // covered by (team_id, create_time DESC)
	t.Index(storm.Desc(&j.CreateTime))
	t.Index(&j.Status)
	t.Index(&j.Team, storm.Desc(&j.CreateTime))
	t.Index(&j.NextRun).Where(storm.RawSQL(`repeat_seconds > 0`))
	t.Check(storm.RawSQL(`length(name) BETWEEN 1 AND 100`))
	t.Check(storm.RawSQL(`status BETWEEN 1 AND 5`))
}

// Worker is a process that runs models for this server. Only its token's
// hash is kept; PublicKey is the Ed25519 key it joined with.
type Worker struct {
	ID          string
	Name        string
	TokenHash   string
	CreatedBy   string
	CreateTime  time.Time
	LastSeen    *time.Time
	PublicKey   []byte
	LastAddress string
}

func (w *Worker) Schema(t *storm.Table) {
	t.Col(&w.TokenHash).Unique()
	t.Col(&w.LastAddress).Default("''")
	t.Check(storm.RawSQL(`length(name) BETWEEN 1 AND 100`))
}

// WorkerSample is one minute of a worker's stats, for its charts.
type WorkerSample struct {
	Worker      Worker
	Time        time.Time
	CPUPercent  float64
	MemoryUsed  int64
	MemoryTotal int64
	DiskUsed    int64
	DiskTotal   int64
	Busy        int64
	Capacity    int64
}

func (s *WorkerSample) Schema(t *storm.Table) {
	t.PrimaryKey(&s.Worker, &s.Time)
	t.Col(&s.Worker).OnDelete(storm.Cascade).NoIndex()
	t.Index(&s.Time)
}

// Usage counts predictions per month, person and key ("" for the web app).
type Usage struct {
	Month       string
	User        User
	KeyID       string
	KeyName     string
	Predictions int64
}

// Joins: counters with their owner's name, busiest first.
func (u *Usage) Joins(j *storm.Joins) {
	var p User
	j.Named("WithUser").
		Inner(&p, &u.User).
		Take(&u.Month, "Month").
		Take(&u.User, "UserID").
		Take(&u.KeyID, "KeyID").
		Take(&u.KeyName, "KeyName").
		Take(&u.Predictions, "Predictions").
		Take(&p.Name, "UserName").
		Take(&p.Email, "Email").
		OrderDesc(&u.Predictions).
		OrderAsc(&p.Email)
}

func (u *Usage) Schema(t *storm.Table) {
	t.Name("usage")
	t.PrimaryKey(&u.Month, &u.User, &u.KeyID)
	t.Col(&u.User).OnDelete(storm.Cascade)
	t.Col(&u.KeyID).Default("''")
	t.Col(&u.KeyName).Default("''")
	t.Col(&u.Predictions).Default("0")
	t.Check(storm.RawSQL(`length(month) = 7`))
}

// All is every model, for storm.Build and migrate.Auto.
func All() []any {
	return []any{
		&Team{}, &User{}, &Model{}, &UserModel{}, &Session{}, &RecoveryCode{},
		&APIKey{}, &APIKeyModel{}, &AuditEvent{}, &Webhook{}, &Delivery{},
		&Setting{}, &Dataset{}, &TrainingJob{}, &Worker{}, &WorkerSample{}, &Usage{},
	}
}
