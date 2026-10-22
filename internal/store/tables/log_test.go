package tables

import (
	"reflect"
	"testing"

	"github.com/gsoultan/storm"
)

// The log database's audit_events must match the main one's column for
// column and index for index, or the generated auditevent package would
// read and write it wrongly.
func TestLogAuditEventMatchesAuditEvent(t *testing.T) {
	main, err := storm.Build(All()...)
	if err != nil {
		t.Fatal(err)
	}
	log, err := storm.Build(LogAll()...)
	if err != nil {
		t.Fatal(err)
	}
	want, got := main.Table("audit_events"), log.Table("audit_events")
	if want == nil || got == nil {
		t.Fatalf("audit_events missing: main %v, log %v", want != nil, got != nil)
	}
	if !reflect.DeepEqual(want.Columns, got.Columns) {
		t.Errorf("columns differ:\nmain %+v\nlog  %+v", want.Columns, got.Columns)
	}
	if !reflect.DeepEqual(want.Indexes, got.Indexes) {
		t.Errorf("indexes differ:\nmain %+v\nlog  %+v", want.Indexes, got.Indexes)
	}
	if len(got.ForeignKeys) != 0 {
		t.Errorf("log audit_events has foreign keys: %+v", got.ForeignKeys)
	}
}
