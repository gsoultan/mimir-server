package tables

import "fmt"

// The closed sets below are stored as int4 with a CHECK built from the
// constants, so adding a value is one constant and one migration. Package
// store aliases each type, so this is their only definition.
//
// They are integers rather than PostgreSQL enums because their order
// means something (a role includes the ones below it; a job status at or
// past JobSucceeded is done), and because the wire API numbers them the
// same way.

// Role is what a user or key may do. Each role includes the ones below it.
type Role int32

const (
	RoleViewer Role = 1 + iota
	RoleEditor
	RoleAdmin
	RoleOwner
)

var roleNames = [...]string{"", "viewer", "editor", "admin", "owner"}

// Valid reports whether r is one of the roles.
func (r Role) Valid() bool { return r >= RoleViewer && r <= RoleOwner }

func (r Role) String() string { return name(roleNames[:], int32(r)) }

// DatasetKind says where a dataset's examples come from.
type DatasetKind int32

const (
	DatasetTable DatasetKind = 1 + iota // a CSV someone uploaded
	DatasetMNIST                        // the built-in handwritten digits
	// DatasetPhotos is a ZIP of photos, one folder per answer.
	DatasetPhotos
	// DatasetPhotoTable is a ZIP of a data.csv and the photos it names.
	DatasetPhotoTable
	// DatasetText is writing a language model learns from: plain text, or
	// chats and prompts with their answers as JSON lines.
	DatasetText
)

var datasetKindNames = [...]string{"", "table", "mnist", "photos", "photo_table", "text"}

// Valid reports whether k is one of the kinds.
func (k DatasetKind) Valid() bool { return k >= DatasetTable && k <= DatasetText }

// Archive reports whether the dataset's file is a ZIP.
func (k DatasetKind) Archive() bool { return k == DatasetPhotos || k == DatasetPhotoTable }

func (k DatasetKind) String() string { return name(datasetKindNames[:], int32(k)) }

// JobStatus is where a training job is in its life.
type JobStatus int32

const (
	JobQueued JobStatus = 1 + iota
	JobRunning
	JobSucceeded
	JobFailed
	JobCancelled
)

var jobStatusNames = [...]string{"", "queued", "running", "succeeded", "failed", "cancelled"}

// Valid reports whether s is one of the statuses.
func (s JobStatus) Valid() bool { return s >= JobQueued && s <= JobCancelled }

// Done reports whether the job is over, whichever way.
func (s JobStatus) Done() bool { return s >= JobSucceeded }

func (s JobStatus) String() string { return name(jobStatusNames[:], int32(s)) }

// DeliveryStatus is where a webhook delivery is in its life.
type DeliveryStatus int32

const (
	DeliveryPending DeliveryStatus = 1 + iota
	DeliveryDelivered
	DeliveryFailed
)

var deliveryStatusNames = [...]string{"", "pending", "delivered", "failed"}

// Valid reports whether s is one of the statuses.
func (s DeliveryStatus) Valid() bool { return s >= DeliveryPending && s <= DeliveryFailed }

func (s DeliveryStatus) String() string { return name(deliveryStatusNames[:], int32(s)) }

// name is the word for v, or the number when v is outside the set.
func name(names []string, v int32) string {
	if v > 0 && int(v) < len(names) {
		return names[v]
	}
	return fmt.Sprintf("%d", v)
}

// between is the CHECK that keeps a column inside its set.
func between[T ~int32](col string, lo, hi T) string {
	return fmt.Sprintf("%s BETWEEN %d AND %d", col, lo, hi)
}
