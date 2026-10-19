package server

import (
	"errors"
	"net/http"

	"connectrpc.com/connect"

	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
)

// DefaultMaxUploads is how many large uploads the server takes at once
// when api.Service.MaxUploads isn't set.
const DefaultMaxUploads = 4

// smallUpload is the size under which an upload that says its length
// skips the cap: a dataset that names a database query, say.
const smallUpload = 1 << 20

// uploadProcedures are the calls that carry a whole file in one message.
// Connect reads each into memory before the handler runs, and converting
// a model takes a few times its size again.
var uploadProcedures = map[string]bool{
	mimirv1connect.ModelServiceCreateModelProcedure:     true,
	mimirv1connect.DatasetServiceCreateDatasetProcedure: true,
}

// uploadGate caps how many uploads are read and handled at once, so a
// burst of them can't take all the server's memory. An upload over the
// cap is refused with ResourceExhausted rather than queued: the client
// already has the file and can try again shortly.
type uploadGate struct {
	slots chan struct{}
	small int64 // uploads that say they are at most this big skip the gate
	errs  *connect.ErrorWriter
}

func newUploadGate(n int, small int64) *uploadGate {
	if n <= 0 {
		n = DefaultMaxUploads
	}
	return &uploadGate{slots: make(chan struct{}, n), small: small, errs: connect.NewErrorWriter()}
}

func (g *uploadGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !uploadProcedures[r.URL.Path] || (r.ContentLength >= 0 && r.ContentLength <= g.small) {
			next.ServeHTTP(w, r)
			return
		}
		select {
		case g.slots <- struct{}{}:
		default:
			err := connect.NewError(connect.CodeResourceExhausted, errors.New("The server is busy with other uploads. Try again in a moment."))
			if g.errs.IsSupported(r) {
				g.errs.Write(w, r, err)
			} else {
				http.Error(w, err.Message(), http.StatusTooManyRequests)
			}
			return
		}
		defer func() { <-g.slots }()
		next.ServeHTTP(w, r)
	})
}
