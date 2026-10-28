package server_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
)

// runDocuments runs the document reader until the test ends.
func runDocuments(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.RunDocuments(ctx, 10*time.Millisecond)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// waitDocument waits for a document to be read, or to fail.
func waitDocument(t *testing.T, c mimirv1connect.DocumentServiceClient, id string) *mimirv1.Document {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		res, err := c.GetDocument(context.Background(), connect.NewRequest(&mimirv1.GetDocumentRequest{Id: id}))
		if err != nil {
			t.Fatal(err)
		}
		d := res.Msg.GetDocument()
		if d.GetStatus() == mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || d.GetStatus() == mimirv1.JobStatus_JOB_STATUS_FAILED {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("document still %v", d.GetStatus())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

const shopGuide = `# Shipping

Parcels leave the warehouse every weekday before noon. Orders made on a weekend leave on Monday.

Express parcels arrive the next day in most cities.

# Pets

Our shop mascot is a zebra called Stripes, who greets visitors at the door every morning.

# Returns

Items can be returned within thirty days with the receipt. Damaged items are refunded in full.
`

// longLanguageModel uploads the tiny GGUF language model as name, made to
// read 4096 tokens rather than 64, so a question fits with its sources.
func longLanguageModel(t *testing.T, c clients, name string) *mimirv1.Model {
	t.Helper()
	b := fixture(t, "tiny-llama.gguf")
	key := []byte("llama.context_length")
	i := bytes.Index(b, key)
	if i < 0 || binary.LittleEndian.Uint32(b[i+len(key):]) != 4 { // a uint32
		t.Fatal("no context length in the fixture")
	}
	binary.LittleEndian.PutUint32(b[i+len(key)+4:], 4096)
	res, err := c.models.CreateModel(context.Background(), connect.NewRequest(&mimirv1.CreateModelRequest{Name: name, Architecture: "mim", Weights: b}))
	if err != nil {
		t.Fatal(err)
	}
	return res.Msg.GetModel()
}

// TestAnswerFromDocuments adds documents to a collection, searches them
// and asks about them, over the API and the OpenAI-compatible one.
func TestAnswerFromDocuments(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)
	docs := mimirv1connect.NewDocumentServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(c.jar))
	runDocuments(t)
	m := longLanguageModel(t, c, "Tiny Llama")

	created, err := docs.CreateCollection(ctx, connect.NewRequest(&mimirv1.CreateCollectionRequest{Name: "Shop help", EmbeddingModelId: m.GetId(), Instructions: "Answer shoppers kindly."}))
	if err != nil {
		t.Fatal(err)
	}
	col := created.Msg.GetCollection()
	if col.GetApiName() != "shop-help" || col.GetLanguageModelId() != m.GetId() || col.GetEmbeddingModelName() != "Tiny Llama" {
		t.Fatalf("collection %v", col)
	}

	// Refusals.
	cnn := createModel(t, c, "Digits", "mnist-mlp")
	for _, bad := range []*mimirv1.CreateCollectionRequest{
		{Name: "", EmbeddingModelId: m.GetId()},
		{Name: "Digits", EmbeddingModelId: cnn.GetId()},
		{Name: "Bad", EmbeddingModelId: m.GetId(), LanguageModelId: cnn.GetId()},
	} {
		if _, err := docs.CreateCollection(ctx, connect.NewRequest(bad)); code(err) != connect.CodeInvalidArgument {
			t.Errorf("CreateCollection(%v): %v", bad, err)
		}
	}
	for _, bad := range []*mimirv1.AddDocumentRequest{
		{CollectionId: col.GetId(), Name: "photo.png", Data: []byte("x")},
		{CollectionId: col.GetId(), Name: "empty.txt"},
		{CollectionId: col.GetId(), Name: "", Data: []byte("x")},
	} {
		if _, err := docs.AddDocument(ctx, connect.NewRequest(bad)); code(err) != connect.CodeInvalidArgument {
			t.Errorf("AddDocument(%q): %v", bad.GetName(), err)
		}
	}
	if _, err := docs.AddDocument(ctx, connect.NewRequest(&mimirv1.AddDocumentRequest{CollectionId: "0190f1f2-0000-7000-8000-000000000000", Name: "a.txt", Data: []byte("x")})); code(err) != connect.CodeNotFound {
		t.Errorf("a document for no collection: %v", err)
	}

	add := func(name, text string) *mimirv1.Document {
		t.Helper()
		res, err := docs.AddDocument(ctx, connect.NewRequest(&mimirv1.AddDocumentRequest{CollectionId: col.GetId(), Name: name, Data: []byte(text)}))
		if err != nil {
			t.Fatal(err)
		}
		return waitDocument(t, docs, res.Msg.GetDocument().GetId())
	}
	guide := add("guide.md", shopGuide)
	if guide.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || guide.GetPassages() < 1 || guide.GetKind() != "markdown" {
		t.Fatalf("guide %v", guide)
	}
	page := add("hours.html", `<html><body><h1>Opening hours</h1><p>The shop opens at nine and closes at six.</p><script>var x</script></body></html>`)
	if page.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || page.GetPassages() != 1 {
		t.Fatalf("page %v", page)
	}
	broken := add("scan.pdf", "%PDF-1.4 not really")
	if broken.GetStatus() != mimirv1.JobStatus_JOB_STATUS_FAILED || !strings.Contains(broken.GetError(), "PDF") {
		t.Fatalf("broken PDF %v", broken)
	}

	got, err := docs.GetCollection(ctx, connect.NewRequest(&mimirv1.GetCollectionRequest{Id: col.GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	if g := got.Msg.GetCollection(); g.GetDocuments() != 3 || g.GetPassages() != guide.GetPassages()+1 || len(got.Msg.GetDocuments()) != 3 {
		t.Fatalf("collection %v with %d documents", g, len(got.Msg.GetDocuments()))
	}
	list, err := docs.ListCollections(ctx, connect.NewRequest(&mimirv1.ListCollectionsRequest{}))
	if err != nil || len(list.Msg.GetCollections()) != 1 || list.Msg.GetCollections()[0].GetDocuments() != 3 {
		t.Fatalf("ListCollections: %v, %v", list, err)
	}

	// The passage that names the zebra comes first for it, by its words.
	found, err := docs.Search(ctx, connect.NewRequest(&mimirv1.SearchRequest{CollectionId: col.GetId(), Query: "Who is the zebra?", Limit: 3}))
	if err != nil {
		t.Fatal(err)
	}
	ps := found.Msg.GetPassages()
	if len(ps) == 0 || !strings.Contains(ps[0].GetText(), "zebra") || ps[0].GetDocumentName() != "guide.md" || ps[0].GetNumber() < 1 {
		t.Fatalf("search found %v", ps)
	}
	// Words match by their stems: "returning" finds "returned".
	found, err = docs.Search(ctx, connect.NewRequest(&mimirv1.SearchRequest{CollectionId: col.GetId(), Query: "returning damaged things", Limit: 1}))
	if err != nil || len(found.Msg.GetPassages()) != 1 || !strings.Contains(found.Msg.GetPassages()[0].GetText(), "Damaged items are refunded") {
		t.Fatalf("search by stems: %v, %v", found, err)
	}
	if _, err := docs.Search(ctx, connect.NewRequest(&mimirv1.SearchRequest{CollectionId: col.GetId(), Query: " "})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("an empty search: %v", err)
	}

	stream, err := docs.Ask(ctx, connect.NewRequest(&mimirv1.AskRequest{
		CollectionId: col.GetId(),
		Messages:     []*mimirv1.ChatMessage{{Role: "user", Content: "When does the shop open?"}},
		MaxTokens:    6,
		Sources:      3,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var first, last *mimirv1.AskResponse
	for stream.Receive() {
		if first == nil {
			first = stream.Msg()
		}
		last = stream.Msg()
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if first == nil || len(first.GetSources()) == 0 || len(first.GetSources()) > 3 {
		t.Fatalf("first answer message %v", first)
	}
	if !last.GetDone() || last.GetGeneratedTokens() == 0 || last.GetPromptTokens() == 0 {
		t.Fatalf("last answer message %v", last)
	}
	bad, err := docs.Ask(ctx, connect.NewRequest(&mimirv1.AskRequest{CollectionId: col.GetId(), Messages: []*mimirv1.ChatMessage{{Role: "assistant", Content: "hi"}}}))
	if err == nil {
		for bad.Receive() {
		}
		err = bad.Err()
	}
	if code(err) != connect.CodeInvalidArgument {
		t.Errorf("Ask without a question: %v", err)
	}

	// Apps that speak the OpenAI API ask a collection by its name.
	key, err := c.keys.CreateApiKey(ctx, connect.NewRequest(&mimirv1.CreateApiKeyRequest{
		Name: "app", Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, ModelIds: []string{m.GetId()}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key.Msg.GetSecret())
		req.Header.Set("Content-Type", "application/json")
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var v map[string]any
		json.NewDecoder(res.Body).Decode(&v)
		if res.StatusCode != 200 {
			t.Fatalf("%s %s: %d %v", method, path, res.StatusCode, v)
		}
		return v
	}
	ids := []string{}
	for _, d := range call("GET", "/v1/models", "")["data"].([]any) {
		ids = append(ids, d.(map[string]any)["id"].(string))
	}
	if strings.Join(ids, ",") != "tiny-llama,shop-help" {
		t.Errorf("models %v", ids)
	}
	answer := call("POST", "/v1/chat/completions", `{"model":"shop-help","max_tokens":4,"messages":[{"role":"user","content":"Can I return a damaged item?"}]}`)
	if answer["model"] != "shop-help" || len(answer["choices"].([]any)) != 1 {
		t.Errorf("chat with a collection: %v", answer)
	}

	// Deleting a document takes its passages from the search.
	if _, err := docs.DeleteDocument(ctx, connect.NewRequest(&mimirv1.DeleteDocumentRequest{Id: guide.GetId()})); err != nil {
		t.Fatal(err)
	}
	found, err = docs.Search(ctx, connect.NewRequest(&mimirv1.SearchRequest{CollectionId: col.GetId(), Query: "zebra"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range found.Msg.GetPassages() {
		if p.GetDocumentId() == guide.GetId() {
			t.Errorf("a deleted document's passage was found: %v", p)
		}
	}
	if _, err := docs.GetDocument(ctx, connect.NewRequest(&mimirv1.GetDocumentRequest{Id: guide.GetId()})); code(err) != connect.CodeNotFound {
		t.Errorf("a deleted document: %v", err)
	}

	upd, err := docs.UpdateCollection(ctx, connect.NewRequest(&mimirv1.UpdateCollectionRequest{Id: col.GetId(), Name: "Shop answers", Instructions: "Be brief."}))
	if err != nil || upd.Msg.GetCollection().GetName() != "Shop answers" || upd.Msg.GetCollection().GetLanguageModelId() != m.GetId() {
		t.Fatalf("UpdateCollection: %v, %v", upd, err)
	}

	// Deleting the embedding model deletes the collection.
	if _, err := c.models.DeleteModel(ctx, connect.NewRequest(&mimirv1.DeleteModelRequest{Id: m.GetId()})); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.GetCollection(ctx, connect.NewRequest(&mimirv1.GetCollectionRequest{Id: col.GetId()})); code(err) != connect.CodeNotFound {
		t.Errorf("a collection whose model is gone: %v", err)
	}
}
