package auth

import (
	"testing"

	"github.com/gsoultan/mimir-server/internal/store"
)

func TestPassword(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "correct horse battery") || CheckPassword(h, "wrong") || CheckPassword("garbage", "x") {
		t.Fatal("CheckPassword")
	}
	if _, err := HashPassword("short"); err != ErrWeakPassword {
		t.Fatalf("weak: %v", err)
	}
}

func TestEffective(t *testing.T) {
	all := store.Access{Role: store.Owner, AllModels: true}
	some := store.Access{Role: store.Editor, ModelIDs: []string{"a", "b"}}
	tests := []struct {
		name      string
		user, key store.Access
		role      store.Role
		allows    map[string]bool
	}{
		{"owner with all-models viewer key", all, store.Access{Role: store.Viewer, AllModels: true}, store.Viewer, map[string]bool{"a": true, "z": true}},
		{"owner with scoped key", all, store.Access{Role: store.Editor, ModelIDs: []string{"a"}}, store.Editor, map[string]bool{"a": true, "b": false}},
		{"scoped user with all-models key", some, store.Access{Role: store.Owner, AllModels: true}, store.Editor, map[string]bool{"a": true, "z": false}},
		{"scoped user with scoped key", some, store.Access{Role: store.Viewer, ModelIDs: []string{"b", "z"}}, store.Viewer, map[string]bool{"a": false, "b": true, "z": false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Effective(tt.user, tt.key)
			if got.Role != tt.role {
				t.Fatalf("role = %v, want %v", got.Role, tt.role)
			}
			for id, want := range tt.allows {
				if got.Allows(id) != want {
					t.Errorf("Allows(%q) = %v, want %v", id, !want, want)
				}
			}
		})
	}
}

func TestEveryProcedureIsClassified(t *testing.T) {
	for proc := range Required {
		if Public[proc] {
			t.Errorf("%s is both public and permissioned", proc)
		}
	}
}
