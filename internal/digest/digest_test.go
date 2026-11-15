package digest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/opsledger/internal/ledger"
	"github.com/nishimoto265/opsledger/internal/store"
)

func TestDigestMarkdownAndJSON(t *testing.T) {
	now := time.Now()
	mk := func(typ, subj string, data map[string]any) ledger.Event {
		e := ledger.NewEvent(typ, "a", subj, "run:1", "test", data)
		e.TS = now
		return e
	}
	st := store.Project([]ledger.Event{
		mk("run.finished", "job:x", map[string]any{"status": "ok"}),
		mk("approval.requested", "thing:y", map[string]any{"what": "deploy"}),
	})
	// fix approval corr key: approval.requested above uses corr run:1 in mk; rebuild with proper corr is fine for digest sections presence
	rep := Build(st, now.Add(-time.Hour), now)
	md := rep.Markdown()
	for _, sec := range []string{"## Summary", "## Completed", "## Stalled / Abnormal", "## Pending Approvals", "## Unreported Delegations & Silent Actors"} {
		if !strings.Contains(md, sec) {
			t.Fatalf("missing section %q in:\n%s", sec, md)
		}
	}
	b, err := rep.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
}
