package s3source

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The examples from AWS's Signature Version 4 docs for S3, which give the
// signature to expect.
func TestSignMatchesAWSExamples(t *testing.T) {
	c := Conn{Region: "us-east-1", AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	when := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, url, rang, want string
	}{
		{"get object", "https://examplebucket.s3.amazonaws.com/test.txt", "bytes=0-9", "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"},
		{"list objects", "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", "", "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, tc.url, nil)
			if tc.rang != "" {
				req.Header.Set("Range", tc.rang)
			}
			sign(req, c, when)
			if got := req.Header.Get("Authorization"); !strings.HasSuffix(got, "Signature="+tc.want) {
				t.Errorf("Authorization = %s, want signature %s", got, tc.want)
			}
		})
	}
}

func TestResolvePresets(t *testing.T) {
	keys := Conn{Bucket: "data", AccessKeyID: "k", SecretAccessKey: "s"}
	r2 := keys
	r2.Provider, r2.AccountID = "r2", "ABC123"
	got, err := r2.Resolve()
	if err != nil || got.Endpoint != "https://abc123.r2.cloudflarestorage.com" || got.Region != "auto" {
		t.Errorf("r2 = %+v, %v", got, err)
	}
	s3 := keys
	s3.Provider, s3.Region = "s3", "eu-west-1"
	got, err = s3.Resolve()
	if err != nil || got.Endpoint != "https://s3.eu-west-1.amazonaws.com" {
		t.Errorf("s3 = %+v, %v", got, err)
	}
	if cl, _ := New(s3); !cl.virtual {
		t.Error("an AWS bucket should be addressed by host name")
	}
	for name, c := range map[string]Conn{
		"r2 without account": {Provider: "r2", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"},
		"other without url":  {Provider: "other", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"},
		"url with a path":    {Provider: "other", Endpoint: "https://x.example/bucket", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"},
		"ftp":                {Provider: "other", Endpoint: "ftp://x.example", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"},
		"no keys":            {Provider: "s3", Bucket: "b"},
		"slash in bucket":    {Provider: "s3", Bucket: "b/c", AccessKeyID: "k", SecretAccessKey: "s"},
	} {
		if _, err := c.Resolve(); !errors.Is(err, ErrBad) {
			t.Errorf("%s: err = %v, want ErrBad", name, err)
		}
	}
}

// fake is a tiny S3: path-style, ListObjectsV2 with paging, and GetObject.
type fake struct {
	mu     sync.Mutex
	bucket string
	files  map[string]string
	page   int
	paths  []string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=key/") {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`<Error><Code>InvalidAccessKeyId</Code></Error>`))
		return
	}
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.EscapedPath())
	f.mu.Unlock()
	rest, ok := strings.CutPrefix(r.URL.Path, "/"+f.bucket)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`<Error><Code>NoSuchBucket</Code></Error>`))
		return
	}
	if rest == "/" && r.URL.Query().Get("list-type") == "2" {
		q := r.URL.Query()
		var keys []string
		for k := range f.files {
			if strings.HasPrefix(k, q.Get("prefix")) && k > q.Get("continuation-token") {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		max := f.page
		if m := q.Get("max-keys"); m != "" {
			var n int
			for _, c := range m {
				n = n*10 + int(c-'0')
			}
			max = min(max, n)
		}
		type content struct {
			Key  string
			Size int
		}
		var out struct {
			XMLName               xml.Name `xml:"ListBucketResult"`
			Contents              []content
			IsTruncated           bool
			NextContinuationToken string
		}
		for i, k := range keys {
			if i == max {
				out.IsTruncated, out.NextContinuationToken = true, keys[i-1]
				break
			}
			out.Contents = append(out.Contents, content{k, len(f.files[k])})
		}
		xml.NewEncoder(w).Encode(out)
		return
	}
	body, ok := f.files[strings.TrimPrefix(rest, "/")]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`<Error><Code>NoSuchKey</Code></Error>`))
		return
	}
	w.Write([]byte(body))
}

func client(t *testing.T, f *fake) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cl, err := New(Conn{Provider: "other", Endpoint: srv.URL, Bucket: f.bucket, AccessKeyID: "key", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

func TestTableJoinsAFolder(t *testing.T) {
	f := &fake{bucket: "shop", page: 2, files: map[string]string{
		"sales/2026-01.csv": "\xef\xbb\xbfa,b\r\n1,x\r\n",
		"sales/2026-02.csv": "a,b\n2,y",
		"sales/2026-03.csv": "a,b\n3,z\n",
		"sales/notes.txt":   "not a table",
		"sales/.hidden.csv": "a,b\n9,no\n",
		"sales2/other.csv":  "c,d\n",
		"one file.csv":      "a,b\n5,q\n",
		"bad/1.csv":         "a,b\n1,x\n",
		"bad/2.csv":         "a,c\n1,x\n",
	}}
	cl := client(t, f)
	ctx := context.Background()
	got, err := cl.Table(ctx, "sales", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if want := "a,b\r\n1,x\r\n2,y\n3,z\n"; string(got) != want {
		t.Errorf("table = %q, want %q", got, want)
	}
	if got, err := cl.Table(ctx, "one file.csv", 1<<20); err != nil || string(got) != "a,b\n5,q\n" {
		t.Errorf("one file = %q, %v", got, err)
	}
	if !slices.Contains(f.paths, "/shop/one%20file.csv") {
		t.Errorf("paths = %v, want the key escaped", f.paths)
	}
	if _, err := cl.Table(ctx, "bad/", 1<<20); !errors.Is(err, ErrBad) || !strings.Contains(err.Error(), "different columns") {
		t.Errorf("mixed headers: %v", err)
	}
	if _, err := cl.Table(ctx, "sales", 10); !errors.Is(err, ErrBad) {
		t.Errorf("over the limit: %v", err)
	}
	if _, err := cl.Table(ctx, "missing/", 1<<20); !errors.Is(err, ErrBad) || !strings.Contains(err.Error(), "nothing is in") {
		t.Errorf("missing: %v", err)
	}
}

func TestArchivePacksAFolder(t *testing.T) {
	f := &fake{bucket: "pics", page: 1000, files: map[string]string{
		"pets/cat/1.jpg": "c1", "pets/cat/2.jpg": "c2", "pets/dog/1.jpg": "d1", "pets/.DS_Store": "x",
	}}
	b, err := client(t, f).Archive(context.Background(), "/pets/", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, z := range zr.File {
		names = append(names, z.Name)
	}
	if want := []string{"cat/1.jpg", "cat/2.jpg", "dog/1.jpg"}; !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestErrorsInWords(t *testing.T) {
	f := &fake{bucket: "real", page: 10, files: map[string]string{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ctx := context.Background()
	wrong, _ := New(Conn{Provider: "other", Endpoint: srv.URL, Bucket: "nope", AccessKeyID: "key", SecretAccessKey: "s"})
	if _, err := wrong.Find(ctx, ""); err == nil || !strings.Contains(err.Error(), "no bucket called nope") {
		t.Errorf("wrong bucket: %v", err)
	}
	keys, _ := New(Conn{Provider: "other", Endpoint: srv.URL, Bucket: "real", AccessKeyID: "other", SecretAccessKey: "s"})
	if _, err := keys.Find(ctx, ""); err == nil || !strings.Contains(err.Error(), "access key") {
		t.Errorf("wrong key: %v", err)
	}
	u, _ := url.Parse(srv.URL)
	srv.Close()
	gone, _ := New(Conn{Provider: "other", Endpoint: "http://" + u.Host, Bucket: "real", AccessKeyID: "key", SecretAccessKey: "s"})
	if _, err := gone.Find(ctx, ""); !errors.Is(err, ErrBad) || !strings.Contains(err.Error(), "couldn't reach") {
		t.Errorf("unreachable: %v", err)
	}
}

// Whoever connects a bucket picks its address, so a storage that answers
// with a redirect can't lead the server elsewhere, such as to the cloud's
// metadata service, and have what it finds read back as a table.
func TestRefusesRedirectsAndMetadata(t *testing.T) {
	secret := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("AccessKeyId,SecretAccessKey\nAKIA,shh\n"))
	}))
	defer secret.Close()
	f := &fake{bucket: "b", page: 10, files: map[string]string{"creds.csv": "x"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/creds.csv") {
			http.Redirect(w, r, secret.URL+"/latest/meta-data/iam/security-credentials/role", http.StatusFound)
			return
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	cl, err := New(Conn{Provider: "other", Endpoint: srv.URL, Bucket: "b", AccessKeyID: "key", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := cl.Table(context.Background(), "creds.csv", 1<<20); !errors.Is(err, ErrBad) || strings.Contains(string(got), "shh") {
		t.Errorf("redirect followed: %q, %v", got, err)
	}

	cl, err = New(Conn{Provider: "other", Endpoint: "http://169.254.169.254", Bucket: "latest", AccessKeyID: "key", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cl.Find(ctx, "meta-data/"); !errors.Is(err, ErrBad) || !strings.Contains(err.Error(), "inside the network") {
		t.Errorf("metadata address: %v", err)
	}
}
