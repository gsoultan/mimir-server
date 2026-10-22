// Package s3source reads examples from S3-compatible storage: AWS S3,
// Cloudflare R2, MinIO and the like. It only lists and downloads; nothing
// is ever written to the bucket.
package s3source

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/gsoultan/mimir-server/internal/netguard"
)

// ErrBad reports a connection or path the caller can fix.
var ErrBad = errors.New("s3source: bad request")

// Driver is the dataset driver name for rows that come from storage.
const Driver = "s3"

// Limits on one fetch.
const (
	MaxFiles = 100_000
	Timeout  = 10 * time.Minute
	// parallel downloads at once, for folders of many small photos.
	parallel = 8
)

// Providers lists the presets, by the name used in the API.
var Providers = []string{"s3", "r2", "other"}

// Conn is how to reach a bucket. Its keys are secrets.
type Conn struct {
	Provider        string `json:"provider"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	AccountID       string `json:"account_id,omitempty"`
	Bucket          string `json:"bucket"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// JSON encodes the connection for keeping on the server.
func (c Conn) JSON() string {
	b, _ := json.Marshal(c)
	return string(b)
}

// Parse reads a connection kept by JSON.
func Parse(s string) (Conn, error) {
	var c Conn
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return Conn{}, fmt.Errorf("%w: the storage connection is damaged; connect it again", ErrBad)
	}
	return c, nil
}

var (
	accountID = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)
	region    = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
)

// Resolve fills in what a provider implies, the endpoint and region, and
// checks the rest.
func (c Conn) Resolve() (Conn, error) {
	c.Provider = strings.TrimSpace(c.Provider)
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	c.Region = strings.TrimSpace(c.Region)
	c.AccountID = strings.TrimSpace(c.AccountID)
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.AccessKeyID = strings.TrimSpace(c.AccessKeyID)
	c.SecretAccessKey = strings.TrimSpace(c.SecretAccessKey)
	switch c.Provider {
	case "s3":
		if c.Region == "" {
			c.Region = "us-east-1"
		}
		if !region.MatchString(c.Region) {
			return Conn{}, fmt.Errorf("%w: %q isn't an AWS region; it looks like us-east-1", ErrBad, c.Region)
		}
		if c.Endpoint == "" {
			c.Endpoint = "https://s3." + c.Region + ".amazonaws.com"
		}
	case "r2":
		if !accountID.MatchString(c.AccountID) {
			return Conn{}, fmt.Errorf("%w: add the Cloudflare account ID; it's on the R2 page of the Cloudflare dashboard", ErrBad)
		}
		c.Endpoint, c.Region = "https://"+strings.ToLower(c.AccountID)+".r2.cloudflarestorage.com", "auto"
	case "other", "":
		c.Provider = "other"
		if c.Endpoint == "" {
			return Conn{}, fmt.Errorf("%w: add the storage address, like https://minio.example.com", ErrBad)
		}
		if c.Region == "" {
			c.Region = "us-east-1"
		}
	default:
		return Conn{}, fmt.Errorf("%w: choose AWS S3, Cloudflare R2 or another S3-compatible storage", ErrBad)
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return Conn{}, fmt.Errorf("%w: the storage address should look like https://storage.example.com, with nothing after the name", ErrBad)
	}
	if c.Bucket == "" || strings.ContainsAny(c.Bucket, "/ ") || len(c.Bucket) > 255 {
		return Conn{}, fmt.Errorf("%w: add the bucket's name, without slashes", ErrBad)
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return Conn{}, fmt.Errorf("%w: add the access key ID and secret access key", ErrBad)
	}
	return c, nil
}

// Object is one file in a bucket.
type Object struct {
	Key  string
	Size int64
}

// Client lists and downloads from one bucket.
type Client struct {
	conn    Conn
	base    *url.URL
	virtual bool // bucket in the host name, as AWS prefers
	http    *http.Client
	now     func() time.Time
}

// New checks c and returns a client for its bucket.
func New(c Conn) (*Client, error) {
	c, err := c.Resolve()
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(c.Endpoint)
	u.Path = ""
	host := u.Hostname()
	// AWS serves new buckets only by name in the host; a name with dots
	// would break its certificate, so those stay in the path.
	virtual := (host == "s3.amazonaws.com" || strings.HasSuffix(host, ".amazonaws.com") && strings.HasPrefix(host, "s3.")) && !strings.Contains(c.Bucket, ".")
	return &Client{conn: c, base: u, virtual: virtual, http: newHTTP(), now: time.Now}, nil
}

// newHTTP returns the client storage is read with. Whoever connects a
// bucket chooses its address, so the client never reaches cloud metadata,
// which would hand out this machine's credentials as a "file", and never
// follows a redirect, which could lead anywhere. Storage on this machine
// or a private network, such as MinIO, is fine.
func newHTTP() *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: netguard.Control(netguard.Metadata)}
	return &http.Client{
		Timeout: Timeout,
		Transport: &http.Transport{
			Proxy:                 nil, // a proxy would dial for us, past the check
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          parallel,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
		// S3 answers a wrong region with an error, not a redirect to follow.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Conn is the resolved connection.
func (cl *Client) Conn() Conn { return cl.conn }

// request builds a signed GET for key (empty for the bucket itself).
func (cl *Client) request(ctx context.Context, key string, q url.Values) (*http.Request, error) {
	u := *cl.base
	p := "/" + escape(key, true)
	if cl.virtual {
		u.Host = cl.conn.Bucket + "." + u.Host
	} else {
		p = "/" + escape(cl.conn.Bucket, false) + p
	}
	raw := u.Scheme + "://" + u.Host + p
	if len(q) > 0 {
		raw += "?" + canonicalQuery(q)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBad, err)
	}
	sign(req, cl.conn, cl.now())
	return req, nil
}

// do sends req and returns the response body, failing on errors the
// storage reports.
func (cl *Client) do(req *http.Request, limit int64) ([]byte, error) {
	res, err := cl.http.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, fmt.Errorf("%w: couldn't reach %s: %v", ErrBad, req.URL.Host, errors.Unwrap(err))
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		return nil, storageError(res.StatusCode, b, cl.conn.Bucket)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: the download stopped: %v", ErrBad, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: a file is bigger than the %d MB that work here", ErrBad, limit>>20)
	}
	return b, nil
}

// storageError turns an S3 error reply into words.
func storageError(status int, body []byte, bucket string) error {
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	xml.Unmarshal(body, &e)
	switch e.Code {
	case "NoSuchBucket":
		return fmt.Errorf("%w: there's no bucket called %s", ErrBad, bucket)
	case "NoSuchKey":
		return fmt.Errorf("%w: that file isn't in the bucket", ErrBad)
	case "InvalidAccessKeyId", "SignatureDoesNotMatch", "InvalidToken", "Unauthorized":
		return fmt.Errorf("%w: the storage didn't accept the access key; check the key ID and secret", ErrBad)
	case "AccessDenied":
		return fmt.Errorf("%w: the access key can't read bucket %s; give it read access", ErrBad, bucket)
	case "AuthorizationHeaderMalformed", "InvalidRegion":
		return fmt.Errorf("%w: the region is wrong for this bucket: %s", ErrBad, e.Message)
	case "PermanentRedirect":
		return fmt.Errorf("%w: the bucket lives in another region; set its region", ErrBad)
	}
	switch {
	case status == http.StatusForbidden || status == http.StatusUnauthorized:
		return fmt.Errorf("%w: the storage refused the access key", ErrBad)
	case status == http.StatusNotFound:
		return fmt.Errorf("%w: the storage has no bucket called %s", ErrBad, bucket)
	case e.Message != "":
		return fmt.Errorf("%w: the storage said: %s", ErrBad, e.Message)
	}
	return fmt.Errorf("%w: the storage answered %d %s", ErrBad, status, http.StatusText(status))
}

// List returns up to max files whose names start with prefix, in order.
func (cl *Client) List(ctx context.Context, prefix string, max int) ([]Object, error) {
	var out []Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if n := max - len(out); n < 1000 {
			q.Set("max-keys", fmt.Sprint(n))
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		req, err := cl.request(ctx, "", q)
		if err != nil {
			return nil, err
		}
		b, err := cl.do(req, 16<<20)
		if err != nil {
			return nil, err
		}
		var page struct {
			Contents []struct {
				Key  string `xml:"Key"`
				Size int64  `xml:"Size"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}
		if err := xml.Unmarshal(b, &page); err != nil {
			return nil, fmt.Errorf("%w: the storage gave a list it couldn't read; is the address an S3-compatible storage?", ErrBad)
		}
		for _, c := range page.Contents {
			out = append(out, Object{Key: c.Key, Size: c.Size})
		}
		if !page.IsTruncated || page.NextContinuationToken == "" || len(out) >= max {
			return out[:min(len(out), max)], nil
		}
		token = page.NextContinuationToken
	}
}

// Get downloads one file, failing when it is bigger than limit.
func (cl *Client) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	req, err := cl.request(ctx, key, nil)
	if err != nil {
		return nil, err
	}
	return cl.do(req, limit)
}

// Files is what a path names: one file, or every file in a folder.
type Files struct {
	// Prefix is the folder, ending in a slash, or empty for one file or
	// the whole bucket.
	Prefix  string
	Objects []Object
	Single  bool
}

// Size is the files' total size.
func (f Files) Size() int64 {
	var n int64
	for _, o := range f.Objects {
		n += o.Size
	}
	return n
}

// Name is a file's name relative to the folder.
func (f Files) Name(o Object) string {
	if f.Single {
		return path.Base(o.Key)
	}
	return strings.TrimPrefix(o.Key, f.Prefix)
}

// Find resolves p to the file it names, or the files in the folder it
// names. Empty means the whole bucket. Hidden files are left out.
func (cl *Client) Find(ctx context.Context, p string) (Files, error) {
	p = strings.TrimLeft(strings.TrimSpace(p), "/")
	if p != "" && !strings.HasSuffix(p, "/") {
		// The file itself lists first, if there is one by that name.
		first, err := cl.List(ctx, p, 1)
		if err != nil {
			return Files{}, err
		}
		if len(first) == 1 && first[0].Key == p {
			return Files{Objects: first, Single: true}, nil
		}
		p += "/"
	}
	all, err := cl.List(ctx, p, MaxFiles+1)
	if err != nil {
		return Files{}, err
	}
	if len(all) > MaxFiles {
		return Files{}, fmt.Errorf("%w: more than %d files are there; pick a smaller folder", ErrBad, MaxFiles)
	}
	f := Files{Prefix: p}
	for _, o := range all {
		if !strings.HasSuffix(o.Key, "/") && !hidden(strings.TrimPrefix(o.Key, p)) {
			f.Objects = append(f.Objects, o)
		}
	}
	if len(f.Objects) == 0 {
		where := "the bucket"
		if p != "" {
			where = p + " in the bucket"
		}
		return Files{}, fmt.Errorf("%w: nothing is in %s", ErrBad, where)
	}
	return f, nil
}

func hidden(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || part == "__MACOSX" {
			return true
		}
	}
	return false
}

// Table downloads a CSV, or every CSV in a folder joined into one. The
// files in a folder must share their header row.
func (cl *Client) Table(ctx context.Context, p string, limit int64) ([]byte, error) {
	f, err := cl.Find(ctx, p)
	if err != nil {
		return nil, err
	}
	if !f.Single {
		f.Objects = slices.DeleteFunc(f.Objects, func(o Object) bool { return !strings.EqualFold(path.Ext(o.Key), ".csv") })
		if len(f.Objects) == 0 {
			return nil, fmt.Errorf("%w: there are no .csv files in %s", ErrBad, f.Prefix)
		}
	}
	if f.Size() > limit {
		return nil, fmt.Errorf("%w: the tables come to %d MB; up to %d MB work here", ErrBad, f.Size()>>20, limit>>20)
	}
	files, err := cl.download(ctx, f.Objects, limit)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	var header string
	for i, b := range files {
		b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
		line, rest, _ := bytes.Cut(b, []byte("\n"))
		h := strings.TrimSuffix(string(line), "\r")
		if i == 0 {
			header = h
			out.Write(b)
		} else {
			if h != header {
				return nil, fmt.Errorf("%w: %s has different columns from %s; every table in the folder needs the same header row", ErrBad, f.Name(f.Objects[i]), f.Name(f.Objects[0]))
			}
			out.Write(rest)
		}
		if out.Len() > 0 && !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
			out.WriteByte('\n')
		}
	}
	return out.Bytes(), nil
}

// Archive downloads a ZIP, or packs a folder into one the way an upload
// would be: names relative to the folder.
func (cl *Client) Archive(ctx context.Context, p string, limit int64) ([]byte, error) {
	f, err := cl.Find(ctx, p)
	if err != nil {
		return nil, err
	}
	if f.Single && !strings.EqualFold(path.Ext(f.Objects[0].Key), ".zip") {
		return nil, fmt.Errorf("%w: pick a .zip file or a folder", ErrBad)
	}
	if f.Size() > limit {
		return nil, fmt.Errorf("%w: the files come to %d MB; up to %d MB work here", ErrBad, f.Size()>>20, limit>>20)
	}
	files, err := cl.download(ctx, f.Objects, limit)
	if err != nil {
		return nil, err
	}
	if f.Single {
		return files[0], nil
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i, o := range f.Objects {
		// Photos are compressed already; storing them saves the time.
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name(o), Method: zip.Store})
		if err != nil {
			return nil, err
		}
		w.Write(files[i])
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// download fetches objs a few at a time, in order.
func (cl *Client) download(ctx context.Context, objs []Object, limit int64) ([][]byte, error) {
	out := make([][]byte, len(objs))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(parallel)
	for i, o := range objs {
		g.Go(func() error {
			b, err := cl.Get(ctx, o.Key, limit)
			if err != nil {
				return fmt.Errorf("%w (reading %s)", err, o.Key)
			}
			out[i] = b
			return nil
		})
	}
	return out, g.Wait()
}

// sign adds an AWS Signature Version 4 to a GET with no body.
func sign(req *http.Request, c Conn, now time.Time) {
	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	t := now.UTC().Format("20060102T150405Z")
	day := t[:8]
	req.Header.Set("x-amz-date", t)
	req.Header.Set("x-amz-content-sha256", emptyHash)

	names := []string{"host"}
	values := map[string]string{"host": req.URL.Host}
	for k := range req.Header {
		k = strings.ToLower(k)
		names = append(names, k)
		values[k] = strings.Join(strings.Fields(req.Header.Get(k)), " ")
	}
	slices.Sort(names)
	var headers strings.Builder
	for _, k := range names {
		headers.WriteString(k + ":" + values[k] + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{
		req.Method, req.URL.EscapedPath(), canonicalQuery(req.URL.Query()), headers.String(), signed, emptyHash,
	}, "\n")
	scope := day + "/" + c.Region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + t + "\n" + scope + "\n" + hexHash(canonical)
	key := mac([]byte("AWS4"+c.SecretAccessKey), day)
	key = mac(key, c.Region)
	key = mac(key, "s3")
	key = mac(key, "aws4_request")
	sig := hex.EncodeToString(mac(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.AccessKeyID+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func mac(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}

func hexHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// canonicalQuery encodes q sorted by name, as SigV4 wants.
func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var parts []string
	for _, k := range keys {
		vs := slices.Clone(q[k])
		slices.Sort(vs)
		for _, v := range vs {
			parts = append(parts, escape(k, false)+"="+escape(v, false))
		}
	}
	return strings.Join(parts, "&")
}

// escape percent-encodes all but the unreserved characters, keeping
// slashes when asked.
func escape(s string, slash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == '~', slash && c == '/':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
