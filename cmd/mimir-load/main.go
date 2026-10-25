// Command mimir-load puts a running server under realistic traffic and
// reports how it held up: requests a second, latency percentiles, errors,
// and the server's memory and goroutines from /metrics while it ran.
//
// It sets the server up (or signs in to it), makes -teams teams each with
// an admin, an API key and the models it predicts with, and then for
// -duration runs, all at once:
//
//   - predictions: -predict callers per team model, each sending the next
//     request as soon as the last is answered;
//   - chat: -chat callers streaming answers from a language model
//     (-lm, a GGUF or .mim file), with time to first token and tokens a
//     second;
//   - training: -train jobs on a generated table, started at the
//     beginning, so predictions and chat share the machine with them;
//   - the console: -browse callers per team listing models and runs with
//     a signed-in session, as people with the web app open do.
//
// For the whole stack, it also starts workers of its own (-train-workers
// and -serve-workers, run from -worker-bin), splits runs across them
// (-group), keeps -train runs going all the time (-churn) with
// -predict-trained callers asking the models they make, follows
// -keep-learning jobs on a database table, and kills a worker every
// -kill-every. -report-every prints how each stretch went, so a long soak
// shows whether memory, goroutines or latency creep up.
//
// Each part is off when its count is 0. Point it at a server started
// against an empty database, or one it set up before (same -owner-email
// and -password):
//
//	MIMIR_SETUP_TOKEN=loadtest-setup-token ./bin/mimir-server -data /tmp/d -database-url postgres://...
//	go run ./cmd/mimir-load -server http://localhost:8080 -setup-token loadtest-setup-token -teams 8 -predict 4 -duration 1m
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/gsoultan/mimir/io/mim"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/arch"
)

type config struct {
	server, setupToken, ownerEmail, password, metricsToken string
	teams, predict, chat, train, browse                    int
	models                                                 []string
	lm                                                     string
	chatTokens, trainRows, trainEpochs                     int
	duration                                               time.Duration

	workerBin, workerLogs                string
	trainWorkers, serveWorkers, group    int
	trainHere                            string
	churn                                bool
	predictTrained, keepModels           int
	keepLearning, keepNewRows            int
	sourceDriver, sourceDSN, sourceQuery string
	killEvery, reportEvery               time.Duration
}

func main() {
	var c config
	var models string
	flag.StringVar(&c.server, "server", "http://localhost:8080", "the server to load")
	flag.StringVar(&c.setupToken, "setup-token", os.Getenv("MIMIR_SETUP_TOKEN"), "the setup token, to set up a new server ($MIMIR_SETUP_TOKEN)")
	flag.StringVar(&c.ownerEmail, "owner-email", "owner@load.test", "the owner to set up or sign in as")
	flag.StringVar(&c.password, "password", "load-test-password-1", "the password of the owner and the team admins")
	flag.StringVar(&c.metricsToken, "metrics-token", os.Getenv("MIMIR_METRICS_TOKEN"), "bearer token for /metrics, when the server needs one")
	flag.IntVar(&c.teams, "teams", 4, "teams, each with its own admin, API key and models")
	flag.StringVar(&models, "models", "mnist-mlp,mnist-cnn", "model types each team predicts with")
	flag.IntVar(&c.predict, "predict", 4, "prediction callers per team and model")
	flag.StringVar(&c.lm, "lm", "", "language model file (GGUF or .mim) to chat with; empty skips chat")
	flag.IntVar(&c.chat, "chat", 4, "chat callers, streaming at once")
	flag.IntVar(&c.chatTokens, "chat-tokens", 64, "tokens each chat answer may write")
	flag.IntVar(&c.train, "train", 2, "training jobs started at the beginning")
	flag.IntVar(&c.trainRows, "train-rows", 20000, "rows in the generated training table")
	flag.IntVar(&c.trainEpochs, "train-epochs", 40, "epochs each training job runs")
	flag.IntVar(&c.browse, "browse", 1, "console callers per team (list models and runs, signed in)")
	flag.DurationVar(&c.duration, "duration", time.Minute, "how long to run")
	flag.StringVar(&c.workerBin, "worker-bin", "", "the mimir-server binary to start -train-workers and -serve-workers with")
	flag.StringVar(&c.workerLogs, "worker-logs", "load-workers", "folder for the workers' data folders and logs")
	flag.IntVar(&c.trainWorkers, "train-workers", 0, "workers to start with -tasks train")
	flag.IntVar(&c.serveWorkers, "serve-workers", 0, "workers to start with -tasks serve")
	flag.IntVar(&c.group, "group", 0, "set Settings → Workers → Train each run on up to this many workers; 0 leaves it")
	flag.StringVar(&c.trainHere, "train-here", "", "set Training on this console: true or false; empty leaves it")
	flag.BoolVar(&c.churn, "churn", false, "keep -train runs going for the whole run, starting the next as each ends, instead of starting -train once")
	flag.IntVar(&c.predictTrained, "predict-trained", 0, "callers asking the models runs make during the load")
	flag.IntVar(&c.keepModels, "keep-models", 10, "with -churn, models from finished runs to keep; older ones are deleted")
	flag.IntVar(&c.keepLearning, "keep-learning", 0, "keep-learning jobs on the -source-query table, each pass waiting for -keep-new-rows new rows")
	flag.IntVar(&c.keepNewRows, "keep-new-rows", 500, "new rows each keep-learning pass waits for")
	flag.StringVar(&c.sourceDriver, "source-driver", "postgres", "the keep-learning source's driver")
	flag.StringVar(&c.sourceDSN, "source-dsn", "", "the keep-learning source's connection string")
	flag.StringVar(&c.sourceQuery, "source-query", "", "the query that reads the keep-learning table; something else must add rows to it")
	flag.DurationVar(&c.killEvery, "kill-every", 0, "kill a random worker this often, and start it again 5 seconds later; 0 never")
	flag.DurationVar(&c.reportEvery, "report-every", 0, "also print how each stretch of this long went; 0 only at the end")
	flag.Parse()
	c.models = strings.Split(models, ",")
	if err := run(context.Background(), c); err != nil {
		log.Fatal(err)
	}
}

// clients is the API as one caller sees it: signed in with a cookie, or
// with an API key when key is set.
type clients struct {
	auth     mimirv1connect.AuthServiceClient
	models   mimirv1connect.ModelServiceClient
	predict  mimirv1connect.PredictServiceClient
	users    mimirv1connect.UserServiceClient
	keys     mimirv1connect.ApiKeyServiceClient
	teams    mimirv1connect.TeamServiceClient
	datasets mimirv1connect.DatasetServiceClient
	training mimirv1connect.TrainingServiceClient
	// workersSvc and settings are for the owner setting up workers.
	workersSvc mimirv1connect.WorkerServiceClient
	settings   mimirv1connect.SettingsServiceClient
	http       *http.Client
	base       string
}

var transport = &http.Transport{
	Proxy:               nil, // a load test talks to the server directly
	DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	MaxIdleConns:        4096,
	MaxIdleConnsPerHost: 4096,
	IdleConnTimeout:     90 * time.Second,
}

func newClients(base, key string) clients {
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Transport: transport, Jar: jar}
	var opts []connect.ClientOption
	if key != "" {
		opts = append(opts, connect.WithInterceptors(bearer(key)))
	}
	return clients{
		auth:       mimirv1connect.NewAuthServiceClient(hc, base, opts...),
		models:     mimirv1connect.NewModelServiceClient(hc, base, opts...),
		predict:    mimirv1connect.NewPredictServiceClient(hc, base, opts...),
		users:      mimirv1connect.NewUserServiceClient(hc, base, opts...),
		keys:       mimirv1connect.NewApiKeyServiceClient(hc, base, opts...),
		teams:      mimirv1connect.NewTeamServiceClient(hc, base, opts...),
		datasets:   mimirv1connect.NewDatasetServiceClient(hc, base, opts...),
		training:   mimirv1connect.NewTrainingServiceClient(hc, base, opts...),
		workersSvc: mimirv1connect.NewWorkerServiceClient(hc, base, opts...),
		settings:   mimirv1connect.NewSettingsServiceClient(hc, base, opts...),
		http:       hc,
		base:       base,
	}
}

// upload sends the file at path to POST /api/uploads, streaming it from
// disk, and returns the id CreateModel takes for it.
func (c clients) upload(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/uploads", f)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct{ Upload, Message string }
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("upload: %s", res.Status)
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upload: %s: %s", res.Status, out.Message)
	}
	return out.Upload, nil
}

type bearer string

func (b bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", "Bearer "+string(b))
		return next(ctx, req)
	}
}

func (b bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", "Bearer "+string(b))
		return conn
	}
}

func (b bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// team is what the load runs as in one team.
type team struct {
	name    string
	session clients // the admin, signed in
	app     clients // the admin's API key
	models  []*mimirv1.Model
}

func run(ctx context.Context, c config) error {
	own := newClients(c.server, "")
	if err := signInOrSetUp(ctx, own, c); err != nil {
		return err
	}
	// Workers first: a -role console answers nothing itself, so the
	// teams' models need somewhere to run before they are warmed.
	var workers *fleet
	if c.trainWorkers+c.serveWorkers > 0 {
		var err error
		workers, err = startFleet(ctx, own, c)
		if workers != nil {
			defer workers.stop(context.Background(), own)
		}
		if err != nil {
			return err
		}
	}
	if err := setTraining(ctx, own, c); err != nil {
		return err
	}
	log.Printf("preparing %d teams", c.teams)
	teams := make([]*team, c.teams)
	for i := range teams {
		t, err := prepareTeam(ctx, own, c, i)
		if err != nil {
			return fmt.Errorf("team %d: %w", i, err)
		}
		teams[i] = t
	}

	var lm *mimirv1.Model
	var lmApp clients
	if c.lm != "" && c.chat > 0 {
		var err error
		if lm, lmApp, err = prepareLanguageModel(ctx, own, c); err != nil {
			return fmt.Errorf("language model: %w", err)
		}
	}

	var jobs []string
	if c.train > 0 && !c.churn {
		var err error
		if jobs, err = startTraining(ctx, own, c); err != nil {
			return fmt.Errorf("training: %w", err)
		}
	}
	var keepers []string
	if c.keepLearning > 0 {
		var err error
		if keepers, err = startKeepLearning(ctx, own, c); err != nil {
			return fmt.Errorf("keep learning: %w", err)
		}
	}

	// Warm every model once, so loading isn't counted as latency.
	for _, t := range teams {
		for _, m := range t.models {
			if _, err := t.app.predict.Predict(ctx, connect.NewRequest(predictRequest(m))); err != nil {
				return fmt.Errorf("warm %s: %w", m.GetName(), err)
			}
		}
	}

	log.Printf("running for %v", c.duration)
	// A cancel rather than a deadline: Connect sends a deadline to the
	// server, which would fail the last requests before the run ends.
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	time.AfterFunc(c.duration, stop)
	start := time.Now()
	stats := map[string]*recorder{}
	rec := func(name string) *recorder {
		if stats[name] == nil {
			stats[name] = &recorder{}
		}
		return stats[name]
	}
	var wg sync.WaitGroup
	for _, t := range teams {
		for _, m := range t.models {
			r := rec("predict " + m.GetArchitecture())
			for range c.predict {
				wg.Go(func() { predictLoop(runCtx, t.app, m, r) })
			}
		}
		r := rec("console list")
		for range c.browse {
			wg.Go(func() { browseLoop(runCtx, t.session, r) })
		}
	}
	chats := &chatRecorder{}
	if lm != nil {
		for range c.chat {
			wg.Go(func() { chatLoop(runCtx, lmApp, lm, c.chatTokens, chats) })
		}
	}
	pool := &trainedPool{}
	runs := &runStats{}
	var misses atomic.Int64
	if c.churn && c.train > 0 {
		wg.Go(func() { churnTraining(runCtx, own, c, pool, runs) })
	}
	if len(keepers) > 0 {
		wg.Go(func() { watchKeepLearning(runCtx, own, keepers, pool, runs) })
	}
	if c.predictTrained > 0 {
		// The owner's key: the models runs make are the owner's.
		key, err := own.keys.CreateApiKey(ctx, connect.NewRequest(&mimirv1.CreateApiKeyRequest{Name: fmt.Sprintf("load trained %d", time.Now().UnixNano()), Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}}))
		if err != nil {
			return err
		}
		app := newClients(c.server, key.Msg.GetSecret())
		r := rec("predict trained")
		for range c.predictTrained {
			wg.Go(func() { predictTrainedLoop(runCtx, app, pool, r, &misses) })
		}
	}
	if workers != nil {
		wg.Go(func() { workers.churn(runCtx, c.killEvery) })
	}
	samples := &metricSampler{url: c.server + "/metrics", token: c.metricsToken, fleet: workers}
	wg.Go(func() { samples.loop(runCtx) })
	if c.reportEvery > 0 {
		wg.Go(func() { interimLoop(runCtx, c.reportEvery, start, stats, samples, workers, runs) })
	}
	wg.Wait()
	elapsed := time.Since(start)

	trained := jobProgress(ctx, own, jobs)
	report(os.Stdout, c, elapsed, stats, chats, samples, trained)
	runs.report(os.Stdout)
	if n := misses.Load(); n > 0 {
		fmt.Printf("predictions to a model deleted as they were sent (not counted above): %d\n", n)
	}
	workers.report(os.Stdout)
	return nil
}

// setTraining applies -train-here and -group to the console's settings.
func setTraining(ctx context.Context, own clients, c config) error {
	if c.trainHere == "" && c.group == 0 {
		return nil
	}
	cur, err := own.settings.GetSettings(ctx, connect.NewRequest(&mimirv1.GetSettingsRequest{}))
	if err != nil {
		return err
	}
	req := &mimirv1.UpdateTrainingRequest{TrainHere: cur.Msg.GetTraining().GetTrainHere()}
	if c.trainHere != "" {
		v, err := strconv.ParseBool(c.trainHere)
		if err != nil {
			return fmt.Errorf("-train-here: %w", err)
		}
		req.TrainHere = v
	}
	if c.group > 0 {
		req.GroupWorkers = new(int32(c.group))
	}
	_, err = own.settings.UpdateTraining(ctx, connect.NewRequest(req))
	return err
}

// interimLoop prints, every period, how the stretch since the last print
// went: requests, latency and errors per part, and memory now.
func interimLoop(ctx context.Context, period time.Duration, start time.Time, stats map[string]*recorder, m *metricSampler, f *fleet, runs *runStats) {
	marks := map[*recorder]int{}
	errMarks := map[*recorder]int{}
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var b strings.Builder
		fmt.Fprintf(&b, "\n--- %v in\n", time.Since(start).Round(time.Second))
		names := make([]string, 0, len(stats))
		for n := range stats {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			r := stats[n]
			r.mu.Lock()
			lat := slices.Clone(r.lat[marks[r]:])
			ne := 0
			for _, k := range r.errs {
				ne += k
			}
			marks[r] = len(r.lat)
			errs := ne - errMarks[r]
			errMarks[r] = ne
			r.mu.Unlock()
			fmt.Fprintf(&b, "%-22s %9d %9.1f/s p50 %9s p99 %9s max %9s errors %d\n", n, len(lat), float64(len(lat))/period.Seconds(), pct(lat, 50), pct(lat, 99), pct(lat, 100), errs)
		}
		m.mu.Lock()
		if k := len(m.rss); k > 0 {
			fmt.Fprintf(&b, "console RSS %.0f MB, heap %.0f MB, goroutines %.0f, open files %.0f\n", m.rss[k-1]/(1<<20), last(m.heap)/(1<<20), last(m.goroutines), last(m.fds))
		}
		m.mu.Unlock()
		if f != nil {
			for _, p := range f.workers {
				p.mu.Lock()
				fmt.Fprintf(&b, "  %-26s RSS %4.0f MB, killed %d\n", p.name, p.lastRSS/(1<<20), p.restarts)
				p.mu.Unlock()
			}
		}
		runs.mu.Lock()
		failed := 0
		for _, k := range runs.failed {
			failed += k
		}
		fmt.Fprintf(&b, "runs: %d started, %d succeeded, %d failed, %d keep-learning passes\n", runs.started, runs.succeeded, failed, runs.passes)
		runs.mu.Unlock()
		fmt.Print(b.String())
	}
}

func last(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return v[len(v)-1]
}

func signInOrSetUp(ctx context.Context, own clients, c config) error {
	state, err := own.auth.GetSetupState(ctx, connect.NewRequest(&mimirv1.GetSetupStateRequest{}))
	if err != nil {
		return err
	}
	if !state.Msg.GetNeedsSetup() {
		if _, err := own.auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: c.ownerEmail, Password: c.password})); err != nil {
			return fmt.Errorf("sign in as %s: %w", c.ownerEmail, err)
		}
		return nil
	}
	if c.setupToken == "" {
		return errors.New("the server needs setting up: give -setup-token")
	}
	_, err = own.auth.Setup(ctx, connect.NewRequest(&mimirv1.SetupRequest{Email: c.ownerEmail, Name: "Load test", Password: c.password, SetupToken: c.setupToken}))
	if err != nil {
		return fmt.Errorf("set up: %w", err)
	}
	return nil
}

func prepareTeam(ctx context.Context, own clients, c config, i int) (*team, error) {
	name := fmt.Sprintf("Load %d", i)
	email := fmt.Sprintf("admin%d@load.test", i)
	t := &team{name: name, session: newClients(c.server, "")}
	// The team and its admin are made on the first run and reused after.
	// Failed sign-ins count against the address, so none are tried.
	tm, err := own.teams.CreateTeam(ctx, connect.NewRequest(&mimirv1.CreateTeamRequest{Name: name}))
	switch {
	case err == nil:
		if _, err := own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{
			Email: email, Name: name + " admin", Password: c.password, TeamId: tm.Msg.GetTeam().GetId(),
			Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_ADMIN},
		})); err != nil {
			return nil, err
		}
	case connect.CodeOf(err) != connect.CodeAlreadyExists:
		return nil, err
	}
	if _, err := t.session.auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: email, Password: c.password})); err != nil {
		return nil, err
	}
	key, err := t.session.keys.CreateApiKey(ctx, connect.NewRequest(&mimirv1.CreateApiKeyRequest{Name: fmt.Sprintf("load %d", time.Now().UnixNano()), Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}}))
	if err != nil {
		return nil, err
	}
	t.app = newClients(c.server, key.Msg.GetSecret())

	have, err := t.session.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{}))
	if err != nil {
		return nil, err
	}
	for _, a := range c.models {
		i := slices.IndexFunc(have.Msg.GetModels(), func(m *mimirv1.Model) bool { return m.GetArchitecture() == a })
		if i >= 0 {
			t.models = append(t.models, have.Msg.GetModels()[i])
			continue
		}
		w, err := weights(ctx, a)
		if err != nil {
			return nil, err
		}
		res, err := t.session.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: name + " " + a, Architecture: a, Weights: w}))
		if err != nil {
			return nil, err
		}
		t.models = append(t.models, res.Msg.GetModel())
	}
	return t, nil
}

func weights(ctx context.Context, name string) ([]byte, error) {
	a, ok := arch.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("no model type %q", name)
	}
	var buf bytes.Buffer
	if err := mim.Save(ctx, &buf, a.New(a.Spec), nil); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func prepareLanguageModel(ctx context.Context, own clients, c config) (*mimirv1.Model, clients, error) {
	have, err := own.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{}))
	if err != nil {
		return nil, clients{}, err
	}
	var lm *mimirv1.Model
	for _, m := range have.Msg.GetModels() {
		if m.GetLanguage() != nil {
			lm = m
		}
	}
	if lm == nil {
		info, err := os.Stat(c.lm)
		if err != nil {
			return nil, clients{}, err
		}
		log.Printf("uploading %s (%d MB)", c.lm, info.Size()>>20)
		t0 := time.Now()
		id, err := own.upload(ctx, c.lm)
		if err != nil {
			return nil, clients{}, err
		}
		res, err := own.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Load chat", Architecture: "mim", Upload: id}))
		if err != nil {
			return nil, clients{}, err
		}
		lm = res.Msg.GetModel()
		log.Printf("uploaded in %v", time.Since(t0).Round(time.Millisecond))
	}
	key, err := own.keys.CreateApiKey(ctx, connect.NewRequest(&mimirv1.CreateApiKeyRequest{Name: fmt.Sprintf("load chat %d", time.Now().UnixNano()), Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}}))
	if err != nil {
		return nil, clients{}, err
	}
	app := newClients(c.server, key.Msg.GetSecret())
	// The first answer loads the model.
	t0 := time.Now()
	s, err := app.predict.Chat(ctx, connect.NewRequest(&mimirv1.ChatRequest{ModelId: lm.GetId(), Messages: []*mimirv1.ChatMessage{{Role: "user", Content: "Hello"}}, MaxTokens: 1}))
	if err != nil {
		return nil, clients{}, err
	}
	for s.Receive() {
	}
	if err := s.Err(); err != nil {
		return nil, clients{}, err
	}
	log.Printf("language model loaded and answered in %v", time.Since(t0).Round(time.Millisecond))
	return lm, app, nil
}

func startTraining(ctx context.Context, own clients, c config) ([]string, error) {
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{
		Name: fmt.Sprintf("Load table %d", time.Now().UnixNano()), Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: table(c.trainRows),
	}))
	if err != nil {
		return nil, err
	}
	var ids []string
	for i := range c.train {
		job, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{
			Name: fmt.Sprintf("Load training %d", i), Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(),
			Settings: &mimirv1.TrainingSettings{Epochs: int32(c.trainEpochs)},
		}))
		if err != nil {
			return nil, err
		}
		ids = append(ids, job.Msg.GetJob().GetId())
	}
	return ids, nil
}

// table is a CSV of rows with 16 number columns, a category, and a class
// that depends on them.
func table(rows int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	var b bytes.Buffer
	for j := range 16 {
		fmt.Fprintf(&b, "f%d,", j)
	}
	b.WriteString("plan,kind\n")
	plans := []string{"free", "team", "business"}
	for range rows {
		sum := 0.0
		for range 16 {
			v := r.NormFloat64()
			sum += v
			fmt.Fprintf(&b, "%.3f,", v)
		}
		p := r.IntN(3)
		kind := "low"
		if sum+float64(p) > 1 {
			kind = "high"
		}
		fmt.Fprintf(&b, "%s,%s\n", plans[p], kind)
	}
	return b.Bytes()
}

type progress struct{ done, running, queued, failed, epochs int }

func jobProgress(ctx context.Context, own clients, ids []string) progress {
	var p progress
	for _, id := range ids {
		j, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: id}))
		if err != nil {
			p.failed++
			continue
		}
		p.epochs += len(j.Msg.GetJob().GetHistory())
		switch j.Msg.GetJob().GetStatus() {
		case mimirv1.JobStatus_JOB_STATUS_SUCCEEDED:
			p.done++
		case mimirv1.JobStatus_JOB_STATUS_RUNNING:
			p.running++
		case mimirv1.JobStatus_JOB_STATUS_QUEUED:
			p.queued++
		default:
			p.failed++
		}
	}
	return p
}

func predictRequest(m *mimirv1.Model) *mimirv1.PredictRequest {
	n := 1
	for _, d := range m.GetInputShape() {
		n *= int(d)
	}
	in := make([]float32, n)
	for i := range in {
		in[i] = rand.Float32()
	}
	return &mimirv1.PredictRequest{ModelId: m.GetId(), Input: &mimirv1.Tensor{Values: in}}
}

func predictLoop(ctx context.Context, app clients, m *mimirv1.Model, r *recorder) {
	req := predictRequest(m)
	for ctx.Err() == nil {
		t0 := time.Now()
		_, err := app.predict.Predict(ctx, connect.NewRequest(req))
		r.add(time.Since(t0), err, ctx)
	}
}

func browseLoop(ctx context.Context, s clients, r *recorder) {
	for ctx.Err() == nil {
		t0 := time.Now()
		_, err := s.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{}))
		if err == nil {
			_, err = s.training.ListJobs(ctx, connect.NewRequest(&mimirv1.ListJobsRequest{}))
		}
		r.add(time.Since(t0), err, ctx)
		// People look at a page for a while before the next.
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

var prompts = []string{
	"Write a short story about a lighthouse keeper.",
	"Explain how a bicycle stays upright.",
	"List five things to pack for a hike.",
	"What makes bread rise?",
}

func chatLoop(ctx context.Context, app clients, lm *mimirv1.Model, tokens int, r *chatRecorder) {
	for ctx.Err() == nil {
		t0 := time.Now()
		s, err := app.predict.Chat(ctx, connect.NewRequest(&mimirv1.ChatRequest{
			ModelId:   lm.GetId(),
			Messages:  []*mimirv1.ChatMessage{{Role: "user", Content: prompts[rand.IntN(len(prompts))]}},
			MaxTokens: int32(tokens), Temperature: new(float32(0.8)),
		}))
		if err != nil {
			r.fail(err, ctx)
			continue
		}
		var first time.Duration
		var generated int32
		for s.Receive() {
			if first == 0 {
				first = time.Since(t0)
			}
			if m := s.Msg(); m.GetDone() {
				generated = m.GetGeneratedTokens()
			}
		}
		if err := s.Err(); err != nil {
			r.fail(err, ctx)
			continue
		}
		r.add(first, time.Since(t0), int(generated))
	}
}

type recorder struct {
	mu     sync.Mutex
	lat    []time.Duration
	errs   map[string]int
	cancel int
}

func (r *recorder) add(d time.Duration, err error, ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case err == nil:
		r.lat = append(r.lat, d)
	case ctx.Err() != nil:
		r.cancel++ // stopped by the end of the run
	default:
		if r.errs == nil {
			r.errs = map[string]int{}
		}
		r.errs[errCode(err)]++
	}
}

func errCode(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code().String() + ": " + ce.Message()
	}
	return err.Error()
}

type chatRecorder struct {
	mu          sync.Mutex
	first, full []time.Duration
	tokens      int
	errs        map[string]int
}

func (r *chatRecorder) add(first, full time.Duration, tokens int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.first = append(r.first, first)
	r.full = append(r.full, full)
	r.tokens += tokens
}

func (r *chatRecorder) fail(err error, ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.errs == nil {
		r.errs = map[string]int{}
	}
	r.errs[errCode(err)]++
}

// metricSampler reads the server's memory and goroutines from /metrics
// every second.
type metricSampler struct {
	url, token string
	fleet      *fleet

	mu         sync.Mutex
	rss, heap  []float64
	goroutines []float64
	fds        []float64
	failed     atomic.Int64
}

func (m *metricSampler) loop(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		m.sample(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (m *metricSampler) sample(ctx context.Context) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, m.url, nil)
	if m.token != "" {
		req.Header.Set("Authorization", "Bearer "+m.token)
	}
	res, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		m.failed.Add(1)
		return
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if m.fleet != nil {
		m.fleet.sample()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for line := range strings.SplitSeq(string(b), "\n") {
		name, v, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			continue
		}
		switch name {
		case "process_resident_memory_bytes":
			m.rss = append(m.rss, f)
		case "go_memstats_heap_inuse_bytes":
			m.heap = append(m.heap, f)
		case "go_goroutines":
			m.goroutines = append(m.goroutines, f)
		case "process_open_fds":
			m.fds = append(m.fds, f)
		}
	}
}

func report(w io.Writer, c config, elapsed time.Duration, stats map[string]*recorder, chats *chatRecorder, m *metricSampler, trained progress) {
	fmt.Fprintf(w, "\n%d teams, %v, %d prediction callers per model, %d chat callers, %d training jobs\n\n",
		c.teams, elapsed.Round(time.Second), c.predict, c.chat, c.train)
	fmt.Fprintf(w, "%-22s %9s %9s %9s %9s %9s %9s %7s\n", "", "requests", "per sec", "p50", "p90", "p99", "max", "errors")
	names := make([]string, 0, len(stats))
	for n := range stats {
		names = append(names, n)
	}
	slices.Sort(names)
	var errs []string
	for _, n := range names {
		r := stats[n]
		ne := 0
		for e, k := range r.errs {
			ne += k
			errs = append(errs, fmt.Sprintf("  %s: %d × %s", n, k, e))
		}
		fmt.Fprintf(w, "%-22s %9d %9.1f %9s %9s %9s %9s %7d\n", n, len(r.lat), float64(len(r.lat))/elapsed.Seconds(),
			pct(r.lat, 50), pct(r.lat, 90), pct(r.lat, 99), pct(r.lat, 100), ne)
	}
	if len(chats.full) > 0 || len(chats.errs) > 0 {
		ne := 0
		for e, k := range chats.errs {
			ne += k
			errs = append(errs, fmt.Sprintf("  chat: %d × %s", k, e))
		}
		fmt.Fprintf(w, "%-22s %9d %9.2f %9s %9s %9s %9s %7d\n", "chat (whole answer)", len(chats.full), float64(len(chats.full))/elapsed.Seconds(),
			pct(chats.full, 50), pct(chats.full, 90), pct(chats.full, 99), pct(chats.full, 100), ne)
		fmt.Fprintf(w, "%-22s %9s %9s %9s %9s %9s %9s\n", "chat (first token)", "", "",
			pct(chats.first, 50), pct(chats.first, 90), pct(chats.first, 99), pct(chats.first, 100))
		fmt.Fprintf(w, "chat tokens written: %d (%.1f a second across all callers)\n", chats.tokens, float64(chats.tokens)/elapsed.Seconds())
	}
	if c.train > 0 && !c.churn {
		fmt.Fprintf(w, "training: %d done, %d learning, %d waiting, %d failed; %d epochs in all\n", trained.done, trained.running, trained.queued, trained.failed, trained.epochs)
	}
	if len(m.rss) > 0 {
		fmt.Fprintf(w, "server memory (RSS): max %.0f MB, last %.0f MB; Go heap in use max %.0f MB, last %.0f MB; goroutines max %.0f, last %.0f; open files max %.0f, last %.0f\n",
			slices.Max(m.rss)/(1<<20), m.rss[len(m.rss)-1]/(1<<20), maxOf(m.heap)/(1<<20), last(m.heap)/(1<<20), maxOf(m.goroutines), last(m.goroutines), maxOf(m.fds), last(m.fds))
	} else {
		fmt.Fprintf(w, "server memory: /metrics unreadable (%d tries failed)\n", m.failed.Load())
	}
	if len(errs) > 0 {
		slices.Sort(errs)
		fmt.Fprintln(w, "errors:")
		for _, e := range errs {
			fmt.Fprintln(w, e)
		}
	}
}

func maxOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return slices.Max(v)
}

func pct(d []time.Duration, p int) string {
	if len(d) == 0 {
		return "-"
	}
	s := slices.Clone(d)
	slices.Sort(s)
	i := (len(s) - 1) * p / 100
	v := s[i]
	switch {
	case v < time.Millisecond:
		return v.Round(time.Microsecond).String()
	case v < time.Second:
		return v.Round(100 * time.Microsecond).String()
	default:
		return v.Round(10 * time.Millisecond).String()
	}
}
