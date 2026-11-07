package arch

import "testing"

func TestEveryModelTypeNamesItsAlgorithm(t *testing.T) {
	for _, a := range All() {
		if a.Algorithm() == "" {
			t.Errorf("%s names no algorithm", a.Name)
		}
	}
}
