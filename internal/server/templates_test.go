package server_test

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
)

// TestTemplates teaches from template examples: a number model asked by
// column name, and a picture model asked with a photo. It also matches a
// table whose columns are named differently.
func TestTemplates(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	tpls := mimirv1connect.NewTemplateServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(own.jar))

	ls, err := tpls.ListTemplates(ctx, connect.NewRequest(&mimirv1.ListTemplatesRequest{}))
	if err != nil || len(ls.Msg.GetTemplates()) != 46 || len(ls.Msg.GetCategories()) != 8 {
		t.Fatalf("ListTemplates = %d, %v", len(ls.Msg.GetTemplates()), err)
	}
	ex, err := tpls.GetTemplateExample(ctx, connect.NewRequest(&mimirv1.GetTemplateExampleRequest{TemplateId: "energy-use"}))
	if err != nil || ex.Msg.GetContentType() != "text/csv" || !strings.HasPrefix(string(ex.Msg.GetData()), "line,planned_units") {
		t.Fatalf("GetTemplateExample = %v", err)
	}

	// A number model from the example data, asked by column name.
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Energy", TemplateId: "energy-use", Example: true}))
	if err != nil {
		t.Fatal(err)
	}
	d := ds.Msg.GetDataset()
	if !d.GetNumberTarget() || d.GetTarget() != "kwh" || !slices.Contains(d.GetArchitectures(), "number-mlp") || ds.Msg.GetCheck().GetVerdict() == "bad" {
		t.Fatalf("energy dataset %v, check %v", d, ds.Msg.GetCheck())
	}
	m := teach(t, own, d.GetId(), "number-mlp", 6)
	if len(m.GetColumns()) != 5 || m.GetColumns()[0].GetKind() != "category" {
		t.Fatalf("model columns %v", m.GetColumns())
	}
	p, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Fields: map[string]string{
		"line": "A", "planned_units": "4200", "outside_temp_c": "31", "hours": "8", "product": "P10",
	}}))
	if err != nil || !p.Msg.GetNumber() || p.Msg.GetValue() <= 0 || len(p.Msg.GetGuesses()) != 0 {
		t.Fatalf("Predict number = %v, %v", p, err)
	}
	if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Fields: map[string]string{"hours": "lots"}})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("word for a number: %v", err)
	}
	if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Fields: map[string]string{"colour": "red"}})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown column: %v", err)
	}
	// No fields at all read as every column empty.
	if p, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId()})); err != nil || !p.Msg.GetNumber() {
		t.Fatalf("Predict with no fields = %v, %v", p, err)
	}

	// A table with differently named columns: the answer is matched by
	// hand, then kept.
	csv := "Machine Type,air_temp_c,process_temp_c,speed,torque_nm,tool_wear_min,days_since_service,broke\n" +
		strings.Repeat("L,25,35,1500,40,100,10,no\nH,26,37,1300,60,200,80,yes\n", 30)
	ds, err = own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Machines", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(csv), TemplateId: "machine-failure"}))
	if err != nil {
		t.Fatal(err)
	}
	ck := ds.Msg.GetCheck()
	if ck.GetVerdict() != "bad" || ck.GetAnswerColumn() != "broke" || ck.GetColumns()[0].GetStatus() != "renamed" || ck.GetColumns()[3].GetStatus() != "missing" || !slices.Contains(ck.GetExtraColumns(), "speed") {
		t.Fatalf("check %v", ck)
	}
	cr, err := own.datasets.CheckDataset(ctx, connect.NewRequest(&mimirv1.CheckDatasetRequest{
		DatasetId: ds.Msg.GetDataset().GetId(), TemplateId: "machine-failure", Apply: true,
		Match: map[string]string{"speed_rpm": "speed", "fails_within_7_days": "broke"},
	}))
	if err != nil || cr.Msg.GetCheck().GetVerdict() == "bad" || cr.Msg.GetDataset().GetTarget() != "broke" || cr.Msg.GetCheck().GetColumns()[3].GetFound() != "speed" {
		t.Fatalf("CheckDataset = %v, %v", cr, err)
	}

	// A picture model from photo folders, asked with a photo.
	ds, err = own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Surfaces", TemplateId: "surface-defect", Example: true}))
	if err != nil {
		t.Fatal(err)
	}
	d = ds.Msg.GetDataset()
	if d.GetKind() != mimirv1.DatasetKind_DATASET_KIND_PHOTOS || len(ds.Msg.GetCheck().GetFolders()) != 4 || !slices.Contains(d.GetArchitectures(), "photo-cnn") {
		t.Fatalf("photo dataset %v, check %v", d, ds.Msg.GetCheck())
	}
	m = teach(t, own, d.GetId(), "photo-cnn", 2)
	ex, _ = tpls.GetTemplateExample(ctx, connect.NewRequest(&mimirv1.GetTemplateExampleRequest{TemplateId: "surface-defect"}))
	zr, _ := zip.NewReader(bytes.NewReader(ex.Msg.GetData()), int64(len(ex.Msg.GetData())))
	f, _ := zr.File[0].Open()
	photo, _ := io.ReadAll(f)
	p, err = own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Photo: photo}))
	if err != nil || len(p.Msg.GetGuesses()) != 3 || p.Msg.GetNumber() {
		t.Fatalf("Predict photo = %v, %v", p, err)
	}
	if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Photo: []byte("not a photo")})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad photo: %v", err)
	}

	// Photos with details: a missing photo is listed and left out.
	ds, err = own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Claims", TemplateId: "warranty-claim", Example: true}))
	if err != nil || ds.Msg.GetDataset().GetKind() != mimirv1.DatasetKind_DATASET_KIND_PHOTO_TABLE || !slices.Contains(ds.Msg.GetDataset().GetArchitectures(), "photo-table") {
		t.Fatalf("photo table = %v, %v", ds, err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("claims/data.csv")
	w.Write([]byte("photo,amount,ok\n" + strings.Repeat("photos/a.png,1,yes\nphotos/b.png,2,no\n", 6) + "photos/gone.png,3,no\n"))
	for _, n := range []string{"a", "b"} {
		w, _ := zw.Create("claims/photos/" + n + ".png")
		w.Write(photoPNG())
	}
	zw.Close()
	ds, err = own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Mine", Kind: mimirv1.DatasetKind_DATASET_KIND_PHOTO_TABLE, Archive: buf.Bytes()}))
	if err != nil || ds.Msg.GetCheck().GetMissingPhotoCount() != 1 || ds.Msg.GetDataset().GetRows() != 12 || ds.Msg.GetCheck().GetMissingPhotos()[0] != "photos/gone.png" {
		t.Fatalf("missing photo = %v, %v", ds, err)
	}
}

// teach trains a model and returns it.
func teach(t *testing.T, own clients, dataset, architecture string, epochs int32) *mimirv1.Model {
	t.Helper()
	ctx := context.Background()
	job, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: architecture, Architecture: architecture, DatasetId: dataset, Settings: &mimirv1.TrainingSettings{Epochs: epochs}}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	j, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: job.Msg.GetJob().GetId()}))
	if err != nil || j.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
		t.Fatalf("job = %v, %v", j, err)
	}
	m, err := own.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: j.Msg.GetJob().GetModelId()}))
	if err != nil {
		t.Fatal(err)
	}
	return m.Msg.GetModel()
}

// photoPNG returns a small grey PNG.
func photoPNG() []byte {
	img := image.NewGray(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 4)
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}
