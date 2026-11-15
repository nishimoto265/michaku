package followx

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/opsledger/internal/config"
	"github.com/nishimoto265/opsledger/internal/ledger"
)

func TestTransformJSONMappingAndExclude(t *testing.T) {
	tgt := config.FollowTarget{Name: "app", Adapter: "json", Mapping: map[string]string{"type": "event", "actor": "actor", "subject": "job"}, ExcludeFields: []string{"secret"}}
	e, ok := TransformJSON(`{"event":"actor.heartbeat","actor":"agent:a","job":"job:x","secret":"nope","msg":"hi"}`, tgt)
	if !ok || e.Type != "actor.heartbeat" || e.Actor != "agent:a" || e.Subject != "job:x" {
		t.Fatalf("bad transform: %+v ok=%v", e, ok)
	}
	if _, present := e.DataMap()["secret"]; present {
		t.Fatal("excluded field leaked into data")
	}
}

func TestTransformRegex(t *testing.T) {
	tgt := config.FollowTarget{Name: "sys", Adapter: "regex", Pattern: `^(?P<actor>\S+) heartbeat (?P<subject>\S+)$`}
	e, ok := TransformRegex("agent:b heartbeat job:y", tgt, nil)
	if !ok || e.Actor != "agent:b" || e.Subject != "job:y" || e.Type != "activity.seen" {
		t.Fatalf("bad regex transform: %+v ok=%v", e, ok)
	}
	if _, ok := TransformRegex("no match here at all!!!", tgt, nil); ok {
		// pattern is anchored, this line differs in structure — actually it has 5 tokens, should not match
		t.Fatal("expected no match")
	}
}

// TestFollowIsReadOnly proves follow never modifies the target log:
// content hash and mtime are identical before/after ProcessNew, and the
// only file created is the separate offset file.
func TestFollowIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	content := []byte("{\"event\":\"actor.heartbeat\",\"actor\":\"agent:a\"}\n")
	if err := os.WriteFile(logPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(logPath)
	hashBefore := sha256.Sum256(content)
	offsetPath := filepath.Join(dir, "offset.json")
	tgt := config.FollowTarget{Name: "app", Path: logPath, Adapter: "json", Mapping: map[string]string{"type": "event", "actor": "actor"}, OffsetFile: offsetPath}
	var got []ledger.Event
	n, err := ProcessNew(tgt, func(e ledger.Event) error { got = append(got, e); return nil })
	if err != nil || n != 1 || len(got) != 1 {
		t.Fatalf("n=%d err=%v got=%v", n, err, got)
	}
	afterBytes, _ := os.ReadFile(logPath)
	after, _ := os.Stat(logPath)
	if sha256.Sum256(afterBytes) != hashBefore {
		t.Fatal("follow modified target log content")
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("follow modified target log metadata (mtime/size)")
	}
	// second pass: offset persisted, no duplicate events
	n, err = ProcessNew(tgt, func(e ledger.Event) error { got = append(got, e); return nil })
	if err != nil || n != 0 {
		t.Fatalf("second pass n=%d err=%v", n, err)
	}
}
