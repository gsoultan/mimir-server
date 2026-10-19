package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/cluster"
	"github.com/gsoultan/mimir-server/internal/store"
)

// SetModelWorker places a model on one worker, or back on automatic.
func (s *Service) SetModelWorker(ctx context.Context, req *connect.Request[mimirv1.SetModelWorkerRequest]) (*connect.Response[mimirv1.SetModelWorkerResponse], error) {
	p, _ := auth.From(ctx)
	m, err := s.get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	wid := req.Msg.GetWorkerId()
	where := "any worker"
	if a, _ := arch.Lookup(m.Architecture); wid != "" && a.Answers() == arch.Written {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Language models run on the console itself, not on workers."))
	}
	if wid != "" {
		w, err := s.worker(ctx, wid)
		if err != nil {
			return nil, err
		}
		where = w.Name
	}
	if err := s.store.SetModelWorker(ctx, m.ID, wid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, notFound(m.ID)
		}
		return nil, s.internal(ctx, err)
	}
	m.WorkerID = wid
	s.cache.Forget(m.ID)
	s.audit(ctx, p.Actor(), "model.worker", m.Name, "runs on "+where)
	return connect.NewResponse(&mimirv1.SetModelWorkerResponse{Model: toProto(m)}), nil
}

// worker returns a registered worker, or NotFound in plain words.
func (s *Service) worker(ctx context.Context, id string) (store.Worker, error) {
	missing := connect.NewError(connect.CodeNotFound, errors.New("That worker doesn't exist anymore. Pick another one."))
	if !store.ValidID(id) {
		return store.Worker{}, missing
	}
	w, err := s.store.GetWorker(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Worker{}, missing
	}
	if err != nil {
		return store.Worker{}, s.internal(ctx, err)
	}
	return w, nil
}

// workerName names a worker for a message, or says it's gone.
func (s *Service) workerName(ctx context.Context, id string) string {
	if w, err := s.store.GetWorker(ctx, id); err == nil {
		return w.Name
	}
	return "a worker that was removed"
}

// ListWorkerChoices is the short list of workers a model or run can be
// placed on.
func (s *Service) ListWorkerChoices(ctx context.Context, _ *connect.Request[mimirv1.ListWorkerChoicesRequest]) (*connect.Response[mimirv1.ListWorkerChoicesResponse], error) {
	ws, err := s.store.ListWorkers(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	live := s.statuses()
	res := &mimirv1.ListWorkerChoicesResponse{}
	for _, w := range ws {
		c := &mimirv1.Worker{Id: w.ID, Name: w.Name}
		for _, st := range live[w.ID] {
			c.Online = true
			c.Device, c.Architectures, c.Trains = st.Hello.Device, st.Hello.Architectures, st.Hello.Trains
			c.Capacity += st.Hello.Capacity
			c.Busy += int32(st.Busy)
		}
		res.Workers = append(res.Workers, c)
	}
	return connect.NewResponse(res), nil
}

// statuses groups the connected links by worker.
func (s *Service) statuses() map[string][]cluster.Status {
	out := map[string][]cluster.Status{}
	if s.Pool == nil {
		return out
	}
	for _, st := range s.Pool.Statuses() {
		out[st.Worker.ID] = append(out[st.Worker.ID], st)
	}
	return out
}

// GetWorker is one worker's dashboard.
func (s *Service) GetWorker(ctx context.Context, req *connect.Request[mimirv1.GetWorkerRequest]) (*connect.Response[mimirv1.GetWorkerResponse], error) {
	w, err := s.worker(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	hours := req.Msg.GetHours()
	switch hours {
	case 0:
		hours = 1
	case 1, 24, 168:
	default:
		return nil, invalid("Chart 1, 24 or 168 hours.")
	}
	models, waiting, err := s.store.Placement(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.GetWorkerResponse{Worker: s.workerView(w, s.statuses()[w.ID], models, waiting)}

	samples, err := s.store.WorkerSamples(ctx, w.ID, time.Now().Add(-time.Duration(hours)*time.Hour), 240)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	for _, x := range samples {
		res.Samples = append(res.Samples, &mimirv1.WorkerSample{
			Time: timestamppb.New(x.Time), CpuPercent: x.CPUPercent,
			MemoryUsedBytes: x.MemoryUsed, MemoryTotalBytes: x.MemoryTotal,
			DiskUsedBytes: x.DiskUsed, DiskTotalBytes: x.DiskTotal,
			Busy: int32(x.Busy), Capacity: int32(x.Capacity),
		})
	}

	// What it's doing now: training runs one by one, predictions counted
	// per model.
	names := map[string]string{}
	name := func(id string, job bool) string {
		if n, ok := names[id]; ok {
			return n
		}
		n := ""
		if job {
			if j, err := s.store.GetJob(ctx, id); err == nil {
				n = j.Name
			}
		} else if m, err := s.store.Get(ctx, id); err == nil {
			n = m.Name
		}
		names[id] = n
		return n
	}
	predicts := map[string]*mimirv1.WorkerTask{}
	for _, st := range s.statuses()[w.ID] {
		for _, t := range st.Tasks {
			if t.Kind == "train" {
				res.Tasks = append(res.Tasks, &mimirv1.WorkerTask{Kind: t.Kind, JobId: t.JobID, JobName: name(t.JobID, true), StartTime: timestamppb.New(t.Started), Count: 1})
				continue
			}
			pt := predicts[t.ModelID]
			if pt == nil {
				pt = &mimirv1.WorkerTask{Kind: t.Kind, ModelId: t.ModelID, ModelName: name(t.ModelID, false), StartTime: timestamppb.New(t.Started)}
				predicts[t.ModelID] = pt
				res.Tasks = append(res.Tasks, pt)
			}
			pt.Count++
			if t.Started.Before(pt.StartTime.AsTime()) {
				pt.StartTime = timestamppb.New(t.Started)
			}
		}
	}

	onIt, err := s.store.ModelsOnWorker(ctx, w.ID)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	for _, it := range onIt {
		res.Models = append(res.Models, &mimirv1.WorkerItem{Id: it.ID, Name: it.Name, Time: timestamppb.New(it.Time)})
	}
	runs, err := s.store.RunsOnWorker(ctx, w.ID, w.Name)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	for _, it := range runs {
		res.Runs = append(res.Runs, &mimirv1.WorkerItem{Id: it.ID, Name: it.Name, Status: jobStatusWord(it.Status), Time: timestamppb.New(it.Time)})
	}
	events, err := s.store.WorkerEvents(ctx, w.Name, 10)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	for _, e := range events {
		res.Events = append(res.Events, &mimirv1.WorkerItem{Id: e.ID, Name: e.Action, Status: e.Detail, Time: timestamppb.New(e.Time)})
	}
	return connect.NewResponse(res), nil
}

func jobStatusWord(status int) string {
	return strings.ToLower(strings.TrimPrefix(mimirv1.JobStatus(status).String(), "JOB_STATUS_"))
}

// workerView is a worker as the Workers pages show it.
func (s *Service) workerView(w store.Worker, live []cluster.Status, models, waiting map[string]int) *mimirv1.Worker {
	pw := workerProto(w)
	pw.Models, pw.Waiting = int32(models[w.ID]), int32(waiting[w.ID])
	if len(w.PublicKey) > 0 {
		pw.KeyFingerprint = Fingerprint(w.PublicKey)
	}
	pw.Address = hostOf(w.LastAddress)
	for _, st := range live {
		pw.Online = true
		pw.LastSeen = timestamppb.Now()
		pw.Device, pw.Cpus, pw.MemoryBytes = st.Hello.Device, st.Hello.Cpus, st.Hello.MemoryBytes
		pw.Architectures, pw.Trains = st.Hello.Architectures, st.Hello.Trains
		pw.Capacity += st.Hello.Capacity
		pw.Busy += int32(st.Busy)
		pw.Version, pw.Address = st.Hello.Version, hostOf(st.Addr)
		pw.Stats, pw.Security, pw.TlsVersion = st.Stats, st.Security, st.TLSVersion
		if pw.ConnectTime == nil || st.Since.Before(pw.ConnectTime.AsTime()) {
			pw.ConnectTime = timestamppb.New(st.Since)
		}
	}
	return pw
}

// ReplaceWorkerToken gives a worker a new token and cuts off the old one.
func (s *Service) ReplaceWorkerToken(ctx context.Context, req *connect.Request[mimirv1.ReplaceWorkerTokenRequest]) (*connect.Response[mimirv1.ReplaceWorkerTokenResponse], error) {
	p, _ := auth.From(ctx)
	w, err := s.worker(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	token, hash, err := auth.NewToken(WorkerPrefix)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if err := s.store.ReplaceWorkerToken(ctx, w.ID, hash); err != nil {
		return nil, s.internal(ctx, err)
	}
	if s.Pool != nil {
		s.Pool.Drop(w.ID)
	}
	s.auditIn(ctx, "", p.Actor(), "worker.token", w.Name, "made a new token; the old one stopped working")
	return connect.NewResponse(&mimirv1.ReplaceWorkerTokenResponse{Token: s.joinToken(token)}), nil
}

// joinToken is what a person copies to a worker: the secret, then the
// console's key fingerprint when there is one to check.
func (s *Service) joinToken(secret string) string {
	if s.ConsolePin == "" {
		return secret
	}
	return secret + "." + s.ConsolePin
}

// Fingerprint is a short, stable name for a key: the start of its SHA-256.
func Fingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])[:16]
}

// PinOf is the fingerprint a join token carries for a certificate: the
// SHA-256 of its public key, so renewing with the same key keeps it.
func PinOf(cert *tls.Certificate) string {
	if cert == nil || cert.Leaf == nil {
		return ""
	}
	sum := sha256.Sum256(cert.Leaf.RawSubjectPublicKeyInfo)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type connKey struct{}

// conn is how a request reached the console.
type conn struct {
	tls       *tls.ConnectionState
	forwarded bool // a trusted proxy says the caller used https
	proxied   bool // a trusted proxy passed the request on
}

// https reports whether the caller reached us over HTTPS.
func (c conn) https() bool { return c.tls != nil || c.forwarded }

// WithConn notes how a request arrived, for the worker link to show and
// check and for the session cookie. Forwarded headers only count from a
// trusted proxy (Service.Proxies).
func (s *Service) WithConn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := conn{
			tls:       r.TLS,
			forwarded: s.Proxies.ForwardedHTTPS(r.Header, r.RemoteAddr),
			proxied:   s.Proxies.Forwarded(r.Header, r.RemoteAddr),
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), connKey{}, c)))
	})
}

// linkSecurity says how a worker's link is protected. A link a trusted
// proxy passed on over plain HTTP is plain even when that proxy runs on
// this machine: the worker is wherever the proxy heard from.
func linkSecurity(ctx context.Context, addr string) (security, version string) {
	c, _ := ctx.Value(connKey{}).(conn)
	switch {
	case c.tls != nil:
		return "tls", tls.VersionName(c.tls.Version)
	case c.forwarded:
		return "proxy", ""
	case c.proxied:
		return "plain", ""
	case loopback(addr):
		return "local", ""
	}
	return "plain", ""
}

func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func hostOf(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// verifyKey checks the key a worker signed with is the one its token
// joined with, and records it on the first join.
func (s *Service) verifyKey(w store.Worker, tokenHash, addr string) func(context.Context, *mimirv1.Hello) error {
	actor := "worker " + w.Name
	return func(ctx context.Context, hello *mimirv1.Hello) error {
		// Read the worker again: its token may have been replaced, and its
		// key forgotten, while this link was saying hello.
		w, err := s.store.WorkerByToken(ctx, tokenHash)
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("That worker token isn't valid anymore."))
		}
		if err != nil {
			return s.internal(ctx, err)
		}
		key := hello.GetPublicKey()
		fp := Fingerprint(key)
		if w.PublicKey == nil {
			set, err := s.store.SetWorkerKey(ctx, w.ID, key)
			if err != nil {
				return s.internal(ctx, err)
			}
			if set {
				s.auditIn(ctx, "", actor, "worker.join", w.Name, fmt.Sprintf("joined from %s with key %s", hostOf(addr), fp))
			} else if w, err = s.store.GetWorker(ctx, w.ID); err != nil {
				return s.internal(ctx, err)
			}
		}
		if w.PublicKey != nil && !bytes.Equal(w.PublicKey, key) {
			s.auditIn(ctx, "", actor, "worker.refused", w.Name, fmt.Sprintf("a different key (%s) tried to connect from %s", fp, hostOf(addr)))
			return connect.NewError(connect.CodePermissionDenied, errors.New("This token already belongs to another machine. If you rebuilt this worker, make a new token for it on its page under Settings → Workers."))
		}
		if prev := hostOf(w.LastAddress); prev != hostOf(addr) {
			s.store.SetWorkerAddress(ctx, w.ID, addr)
			if prev != "" && w.PublicKey != nil {
				s.auditIn(ctx, "", actor, "worker.address", w.Name, fmt.Sprintf("now connects from %s instead of %s", hostOf(addr), prev))
			}
		} else if w.LastAddress != addr {
			s.store.SetWorkerAddress(ctx, w.ID, addr)
		}
		return nil
	}
}

// KeepSample keeps a worker's stats for its charts.
func (s *Service) KeepSample(workerID string, x store.WorkerSample) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.AddWorkerSample(ctx, workerID, x); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Warn("worker: keep stats", "error", err)
	}
}
