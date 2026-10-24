package server

import (
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
)

// DefaultMaxUploads is how many large uploads the server takes at once
// when api.Service.MaxUploads isn't set.
const DefaultMaxUploads = 4

// smallUpload is the size under which an upload that says its length
// skips the cap: a dataset that names a database query, say.
const smallUpload = 1 << 20

// uploadProcedures are the calls that carry a whole file: the RPCs that
// take one in a message, which Connect reads into memory before the
// handler runs, and POST /api/uploads, which writes one to disk as it
// arrives and so takes disk bandwidth rather than memory.
var uploadProcedures = map[string]bool{
	mimirv1connect.ModelServiceCreateModelProcedure:     true,
	mimirv1connect.DatasetServiceCreateDatasetProcedure: true,
	"/api/uploads": true,
}

// An upload holding a slot must keep sending: it gets uploadGrace plus a
// second for every uploadMinRate bytes it says it sends (or the largest
// upload allowed, when it doesn't say) before its body is cut off.
// Without that bound, anyone able to open a connection could hold every
// slot by sending a large upload a byte at a time, before signing in:
// Connect reads a whole message before the interceptor sees who sent it.
const (
	uploadGrace   = 2 * time.Minute
	uploadMinRate = 1 << 20 // bytes per second
)

// uploadGate caps how many uploads are read and handled at once, so a
// burst of them can't take all the server's memory. An upload over the
// cap is refused with ResourceExhausted rather than queued: the client
// already has the file and can try again shortly.
type uploadGate struct {
	slots chan struct{}
	small int64 // uploads that say they are at most this big skip the gate
	max   int64 // the largest upload, for ones that don't say their size
	// patience is how long an upload of size bytes may take to arrive.
	patience func(size int64) time.Duration
	errs     *connect.ErrorWriter
	// lowMemory, when set, says memory is too short to read an upload
	// into it; uploads streamed to disk still go through.
	lowMemory func() bool
}

func newUploadGate(n int, small, max int64) *uploadGate {
	if n <= 0 {
		n = DefaultMaxUploads
	}
	return &uploadGate{slots: make(chan struct{}, n), small: small, max: max, patience: uploadPatience, errs: connect.NewErrorWriter()}
}

func uploadPatience(size int64) time.Duration {
	return uploadGrace + time.Duration(size/uploadMinRate)*time.Second
}

func (g *uploadGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !uploadProcedures[r.URL.Path] || (r.ContentLength >= 0 && r.ContentLength <= g.small) {
			next.ServeHTTP(w, r)
			return
		}
		if g.lowMemory != nil && r.URL.Path != "/api/uploads" && g.lowMemory() {
			w.Header().Set("Retry-After", "30")
			g.refuse(w, r, connect.NewError(connect.CodeUnavailable, errors.New("The server is low on memory right now. Try the upload again in a minute.")), http.StatusServiceUnavailable)
			return
		}
		select {
		case g.slots <- struct{}{}:
		default:
			g.refuse(w, r, connect.NewError(connect.CodeResourceExhausted, errors.New("The server is busy with other uploads. Try again in a moment.")), http.StatusTooManyRequests)
			return
		}
		defer func() { <-g.slots }()
		size := r.ContentLength
		if size < 0 || size > g.max {
			size = g.max
		}
		// Best effort: a writer that can't set a deadline reports so, and
		// the upload then runs as before.
		http.NewResponseController(w).SetReadDeadline(time.Now().Add(g.patience(size)))
		next.ServeHTTP(w, r)
	})
}

// refuse answers with err, in Connect's shape where the client speaks it.
func (g *uploadGate) refuse(w http.ResponseWriter, r *http.Request, err *connect.Error, status int) {
	if g.errs.IsSupported(r) {
		g.errs.Write(w, r, err)
		return
	}
	http.Error(w, err.Message(), status)
}
