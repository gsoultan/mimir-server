package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/auth"
)

// A large model comes in two steps. POST /api/uploads writes the file to
// disk as it arrives and answers with an id; CreateModel then names that
// id in place of the file's bytes. Neither step holds the file in memory,
// where a model sent inside CreateModel is read whole before anything
// else happens.

// uploadTTL is how long an upload waits to be used before it is removed.
const uploadTTL = time.Hour

// maxWaiting is how many unused uploads one caller may have at once, so
// uploads nobody uses can't fill the disk.
const maxWaiting = 4

// uploadBuffer is how much of an upload is read from the network before
// it is written to disk: large enough that a gigabyte takes a thousand
// writes, not tens of thousands.
const uploadBuffer = 1 << 20

var uploadBuffers = sync.Pool{New: func() any { return new([uploadBuffer]byte) }}

// uploads holds the files sent to POST /api/uploads until CreateModel
// uses them or they expire.
type uploads struct {
	mu      sync.Mutex
	waiting map[string]*upload
}

type upload struct {
	path  string
	owner string
	timer *time.Timer
}

var errTooManyUploads = errors.New("too many uploads waiting")

// add keeps the file at path for owner and returns its id.
func (u *uploads) add(path, owner string) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, w := range u.waiting {
		if w.owner == owner {
			n++
		}
	}
	if n >= maxWaiting {
		return "", errTooManyUploads
	}
	var b [16]byte
	rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	if u.waiting == nil {
		u.waiting = map[string]*upload{}
	}
	w := &upload{path: path, owner: owner}
	w.timer = time.AfterFunc(uploadTTL, func() {
		u.mu.Lock()
		if u.waiting[id] == w {
			delete(u.waiting, id)
		}
		u.mu.Unlock()
		os.Remove(path)
	})
	u.waiting[id] = w
	return id, nil
}

// take hands the file of upload id to its owner, who removes it once
// done. Nobody else can take it, and it can be taken once.
func (u *uploads) take(id, owner string) (string, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	w, ok := u.waiting[id]
	if !ok || w.owner != owner || !w.timer.Stop() {
		return "", false
	}
	delete(u.waiting, id)
	return w.path, true
}

// uploader names who sent an upload: an API key, or a person signed in.
func uploader(p auth.Principal) string {
	if p.Key != nil {
		return "key " + p.Key.ID
	}
	return "user " + p.User.ID
}

// UploadHandler serves POST /api/uploads, which takes a model file of up
// to limit bytes as the request body and answers {"upload": id, "size":
// bytes} for CreateModel. It needs what CreateModel needs: a signed-in
// person or an API key that may add models.
func (s *Service) UploadHandler(limit int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, err := auth.Check(r.Context(), s.store, s.MustEnroll, mimirv1connect.ModelServiceCreateModelProcedure, r.Header, r.RemoteAddr)
		if err != nil {
			uploadError(w, err)
			return
		}
		p, _ := auth.From(ctx)
		over := connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("This file is larger than the %s this server takes. An owner can raise the limit with -max-model-mb.", megabytes(limit)))
		if r.ContentLength > limit {
			uploadError(w, over)
			return
		}
		f, err := s.store.TempFile()
		if err != nil {
			uploadError(w, s.internal(ctx, err))
			return
		}
		keep := false
		defer func() {
			if !keep {
				f.Close()
				os.Remove(f.Name())
			}
		}()
		buf := uploadBuffers.Get().(*[uploadBuffer]byte)
		defer uploadBuffers.Put(buf)
		// The file is wrapped so the copy goes through buf rather than the
		// file's own ReadFrom, which reads the network 32 KB at a time.
		n, err := io.CopyBuffer(struct{ io.Writer }{f}, io.LimitReader(r.Body, limit+1), buf[:])
		switch {
		case err != nil:
			s.log.InfoContext(ctx, "upload not received", "bytes", n, "error", err)
			uploadError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("The upload stopped before the whole file arrived. Try again.")))
			return
		case n > limit:
			uploadError(w, over)
			return
		case n == 0:
			uploadError(w, invalid("The model file is empty."))
			return
		}
		if err := f.Close(); err != nil {
			uploadError(w, s.internal(ctx, err))
			return
		}
		id, err := s.uploads.add(f.Name(), uploader(p))
		if err != nil {
			uploadError(w, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("You have %d uploads waiting to be added as models. Add them, or wait an hour for them to expire.", maxWaiting)))
			return
		}
		keep = true
		s.log.InfoContext(ctx, "upload received", "bytes", n)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(struct {
			Upload string `json:"upload"`
			Size   int64  `json:"size"`
		}{id, n})
	})
}

// uploadError answers an upload with err in Connect's error shape, so
// clients read its message as they do an RPC's.
func uploadError(w http.ResponseWriter, err error) {
	ce := new(connect.Error)
	if !errors.As(err, &ce) {
		ce = connect.NewError(connect.CodeInternal, err)
	}
	status := map[connect.Code]int{
		connect.CodeInvalidArgument:    http.StatusBadRequest,
		connect.CodeUnauthenticated:    http.StatusUnauthorized,
		connect.CodePermissionDenied:   http.StatusForbidden,
		connect.CodeNotFound:           http.StatusNotFound,
		connect.CodeFailedPrecondition: http.StatusPreconditionFailed,
		connect.CodeResourceExhausted:  http.StatusRequestEntityTooLarge,
		connect.CodeInternal:           http.StatusInternalServerError,
	}[ce.Code()]
	if status == 0 {
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{ce.Code().String(), ce.Message()})
}

// megabytes writes n bytes for people: 256 MB, 2 GB.
func megabytes(n int64) string {
	if n >= 1<<30 && n%(1<<30) == 0 {
		return fmt.Sprintf("%d GB", n>>30)
	}
	return fmt.Sprintf("%d MB", n>>20)
}
