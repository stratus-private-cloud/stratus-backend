package disk_test

import (
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestAPutSurvivesADeletePruningItsDirectory is #570: a Delete of the last
// object in a directory removes the directory, and a Put that had just made it
// found it gone when it renamed into it.
func TestAPutSurvivesADeletePruningItsDirectory(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)

	const rounds = 500
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range rounds {
			key := "image/2026/11/22/gone" + strconv.Itoa(i)
			if _, err := s.Put(t.Context(), key, strings.NewReader("x"), 1); err != nil {
				t.Errorf("Put %q = %v", key, err)
				return
			}
			if err := s.Delete(t.Context(), key); err != nil {
				t.Errorf("Delete %q = %v", key, err)
				return
			}
		}
	})
	wg.Go(func() {
		for i := range rounds {
			key := "image/2026/11/22/kept" + strconv.Itoa(i)
			if _, err := s.Put(t.Context(), key, strings.NewReader("y"), 1); err != nil {
				t.Errorf("Put %q = %v", key, err)
				return
			}
			if err := s.Delete(t.Context(), key); err != nil {
				t.Errorf("Delete %q = %v", key, err)
				return
			}
		}
	})
	wg.Wait()
}
