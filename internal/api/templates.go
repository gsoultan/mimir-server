package api

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/templates"
	"github.com/gsoultan/mimir-server/internal/training"
)

var _ mimirv1connect.TemplateServiceHandler = (*Service)(nil)

func (s *Service) ListTemplates(context.Context, *connect.Request[mimirv1.ListTemplatesRequest]) (*connect.Response[mimirv1.ListTemplatesResponse], error) {
	cats, tpls := templates.All()
	res := &mimirv1.ListTemplatesResponse{}
	for _, c := range cats {
		res.Categories = append(res.Categories, &mimirv1.TemplateCategory{Id: c.ID, Label: c.Label, Blurb: c.Blurb, Icon: c.Icon})
	}
	for _, t := range tpls {
		res.Templates = append(res.Templates, templateProto(t))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) GetTemplateExample(ctx context.Context, req *connect.Request[mimirv1.GetTemplateExampleRequest]) (*connect.Response[mimirv1.GetTemplateExampleResponse], error) {
	t, err := template(req.Msg.GetTemplateId())
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, invalid("Pick a template.")
	}
	name, ct, data, err := templates.Example(*t)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.GetTemplateExampleResponse{FileName: name, ContentType: ct, Data: data}), nil
}

func (s *Service) CheckDataset(ctx context.Context, req *connect.Request[mimirv1.CheckDatasetRequest]) (*connect.Response[mimirv1.CheckDatasetResponse], error) {
	p, _ := auth.From(ctx)
	d, err := s.getDataset(ctx, req.Msg.GetDatasetId())
	if err != nil {
		return nil, err
	}
	tpl, err := template(req.Msg.GetTemplateId())
	if err != nil {
		return nil, err
	}
	if d.Kind == store.DatasetMNIST {
		return nil, invalid("The built-in handwritten digits are ready as they are.")
	}
	b, err := s.store.ReadDataset(d.ID)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	match := req.Msg.GetMatch()
	target := ""
	if tpl == nil {
		target = d.Target
	}
	var nd store.Dataset
	var check *mimirv1.DataCheck
	switch d.Kind {
	case store.DatasetTable:
		nd, check, err = describeTable(b, target, tpl, match)
	case store.DatasetPhotos:
		nd, check, err = describePhotos(b, tpl)
	case store.DatasetPhotoTable:
		nd, check, err = describePhotoTable(b, target, tpl, match)
	}
	if err != nil {
		return nil, err
	}
	if req.Msg.GetApply() {
		d.Target, d.Rows, d.Features, d.Labels, d.InputShape = nd.Target, nd.Rows, nd.Features, nd.Labels, nd.InputShape
		d.Columns, d.NumberTarget, d.Skipped = nd.Columns, nd.NumberTarget, nd.Skipped
		if d, err = s.store.UpdateDatasetColumns(ctx, d); err != nil {
			return nil, s.internal(ctx, err)
		}
		s.audit(ctx, p.Actor(), "dataset.columns", d.Name, "answer: "+d.Target)
	}
	return connect.NewResponse(&mimirv1.CheckDatasetResponse{Check: check, Dataset: datasetProto(d)}), nil
}

// template returns the template with id, nil for an empty id, or an
// error naming a template that doesn't exist.
func template(id string) (*templates.Template, error) {
	if id == "" {
		return nil, nil
	}
	t, ok := templates.Get(id)
	if !ok {
		return nil, invalid("There's no template called %q.", id)
	}
	return &t, nil
}

// kindOf is the dataset kind a template's data makes.
func kindOf(t templates.Template) store.DatasetKind {
	switch t.Data {
	case templates.Photos:
		return store.DatasetPhotos
	case templates.PhotoTable:
		return store.DatasetPhotoTable
	}
	return store.DatasetTable
}

// badTable turns a training.ErrBadTable into a message for people.
func badTable(err error) error {
	if errors.Is(err, training.ErrBadTable) {
		return invalid("%s", strings.TrimPrefix(err.Error(), "training: bad table: "))
	}
	return err
}

// parseMatched parses a table, reading its columns as tpl says when
// given. given pairs template columns with differently named ones.
func parseMatched(b []byte, target string, tpl *templates.Template, given map[string]string, hints map[string]schema.Kind) (*training.Table, map[string]string, error) {
	var match map[string]string
	if tpl != nil {
		var header []string
		if rows := training.Sample(b, 0); len(rows) > 0 {
			header = rows[0]
		}
		match = tpl.Match(header, given)
		if target == "" {
			target = match[tpl.Answer().Name]
		}
		hints = tpl.Hints(match)
	}
	t, err := training.ParseTable(bytes.NewReader(b), target, hints)
	return t, match, badTable(err)
}

// describeTable reads an uploaded table into a dataset row and checks
// it, against tpl when given.
func describeTable(b []byte, target string, tpl *templates.Template, given map[string]string) (store.Dataset, *mimirv1.DataCheck, error) {
	t, match, err := parseMatched(b, target, tpl, given, nil)
	if err != nil {
		return store.Dataset{}, nil, err
	}
	if i := schema.PhotoColumn(t.Columns); i >= 0 {
		if tpl != nil && tpl.Data == templates.PhotoTable {
			return store.Dataset{}, nil, invalid("This template looks at photos too. Upload a ZIP with the table as data.csv and a photos folder.")
		}
		// File names that look like photos, with no photos to go with
		// them: read them as words.
		hints := map[string]schema.Kind{t.Columns[i].Name: schema.Text}
		if tpl != nil {
			for k, v := range tpl.Hints(match) {
				if k != t.Columns[i].Name {
					hints[k] = v
				}
			}
		}
		if t, err = training.ParseTable(bytes.NewReader(b), t.Target, hints); err != nil {
			return store.Dataset{}, nil, badTable(err)
		}
	}
	d := store.Dataset{Kind: store.DatasetTable}
	t.Describe(&d)
	return d, templates.CheckTable(tpl, t, match, nil), nil
}

// describePhotoTable reads a ZIP of a data.csv and its photos.
func describePhotoTable(b []byte, target string, tpl *templates.Template, given map[string]string) (store.Dataset, *mimirv1.DataCheck, error) {
	if len(b) == 0 {
		return store.Dataset{}, nil, invalid("The file is empty. Upload a ZIP with data.csv and a photos folder.")
	}
	arc, err := training.OpenArchive(b)
	if err != nil {
		return store.Dataset{}, nil, badTable(err)
	}
	name, csvBytes, err := arc.Table()
	if err != nil {
		return store.Dataset{}, nil, badTable(err)
	}
	t, match, err := parseMatched(csvBytes, target, tpl, given, nil)
	if err != nil {
		return store.Dataset{}, nil, err
	}
	i := schema.PhotoColumn(t.Columns)
	if i < 0 {
		return store.Dataset{}, nil, invalid("No column of %s names the photos. Add a column with each row's photo file, like photos/1001.jpg.", name)
	}
	var missing []string
	for _, cell := range t.Photos {
		if arc.Find(name, cell) == "" {
			missing = append(missing, cell)
		}
	}
	if t.Rows-len(missing) < training.MinRows {
		return store.Dataset{}, nil, invalid("Only %d rows have their photo in the ZIP; at least %d are needed. Check the %s column names files in the ZIP.", t.Rows-len(missing), training.MinRows, t.Columns[i].Name)
	}
	if t.Rows-len(missing) > training.MaxPhotos {
		return store.Dataset{}, nil, invalid("At most %d photos work here.", training.MaxPhotos)
	}
	d := store.Dataset{Kind: store.DatasetPhotoTable}
	t.Describe(&d)
	d.Rows -= int64(len(missing))
	d.Skipped += int64(len(missing))
	check := templates.CheckTable(tpl, t, match, missing)
	check.Rows = d.Rows
	return d, check, nil
}

// describePhotos reads a ZIP of photo folders.
func describePhotos(b []byte, tpl *templates.Template) (store.Dataset, *mimirv1.DataCheck, error) {
	if len(b) == 0 {
		return store.Dataset{}, nil, invalid("The file is empty. Upload a ZIP with one folder of photos per answer.")
	}
	arc, err := training.OpenArchive(b)
	if err != nil {
		return store.Dataset{}, nil, badTable(err)
	}
	folders, loose := arc.Folders()
	labels, err := training.PhotoLabels(folders)
	if err != nil {
		return store.Dataset{}, nil, badTable(err)
	}
	check := templates.CheckPhotos(tpl, folders, loose)
	return store.Dataset{Kind: store.DatasetPhotos, Rows: check.Rows, Labels: labels, Skipped: int64(loose),
		InputShape: []int{3, schema.PhotoSize, schema.PhotoSize}}, check, nil
}

func templateProto(t templates.Template) *mimirv1.Template {
	out := &mimirv1.Template{
		Id: t.ID, Category: t.Category, Kind: t.Kind, Data: t.Data, Name: t.Name, Question: t.Question,
		Predicts: t.Predicts, RowMeans: t.RowMeans, Source: t.Source, SourceShort: t.SourceShort,
		RowsMin: t.RowsMin, RowsGood: t.RowsGood, RowsGreat: t.RowsGreat, Balance: t.Balance, Table: t.Table,
		Tips: t.Tips, Architecture: t.Architecture(), Query: t.Query(),
	}
	for _, c := range t.Columns {
		out.Columns = append(out.Columns, &mimirv1.TemplateColumn{Name: c.Name, Kind: string(c.Kind), Meaning: c.Meaning, Examples: c.Examples})
	}
	for _, f := range t.Folders {
		out.Folders = append(out.Folders, &mimirv1.TemplateFolder{Name: f.Name, Meaning: f.Meaning, Count: f.Count})
	}
	return out
}
