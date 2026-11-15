package ledger

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestAppendAndRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l.jsonl")
	e := NewEvent("actor.heartbeat", "agent:x", "agent:x", "", "test", map[string]any{"note": "hi"})
	if err := Append(p, e); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAll(p)
	if err != nil || len(got) != 1 || got[0].ID != e.ID {
		t.Fatalf("got %v err %v", got, err)
	}
}

func TestAppendConcurrentLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l.jsonl")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = Append(p, NewEvent("actor.heartbeat", "a", "a", "", "test", nil))
		}()
	}
	wg.Wait()
	got, err := ReadAll(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 {
		t.Fatalf("expected 20 lines intact, got %d (interleaving/corruption?)", len(got))
	}
}

func TestValidateRejectsUnknownType(t *testing.T) {
	e := NewEvent("bogus.type", "a", "s", "", "t", nil)
	if err := Validate(e); err == nil {
		t.Fatal("expected error")
	}
}
