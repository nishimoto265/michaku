package digest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/michaku/internal/ledger"
	"github.com/nishimoto265/michaku/internal/store"
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

func TestDigestPartialRunFinished(t *testing.T) {
	now := time.Now()
	mk := func(subj string, data map[string]any) ledger.Event {
		e := ledger.NewEvent("run.finished", "agent:daily-agent", subj, "run:1", "test", data)
		e.TS = now
		return e
	}
	sites := []string{"Site A", "Site B", "Site C", "Site D game"}
	st := store.Project([]ledger.Event{
		mk("job:daily-routine", map[string]any{
			"status":           "ok",
			"partial":          true,
			"sites_incomplete": []any{"Site A", "Site B", "Site C", "Site D game"},
			"note":             "some sites could not be completed",
		}),
		mk("job:other", map[string]any{"status": "ok"}),
	})
	rep := Build(st, now.Add(-time.Hour), now.Add(time.Hour))

	// Markdown / Completed strings: the partial run must read as
	// "completed but incomplete", with the unfinished breakdown and note.
	md := rep.Markdown()
	if !strings.Contains(md, "## Completed") {
		t.Fatalf("missing Completed section in:\n%s", md)
	}
	var partialLine, normalLine string
	for _, line := range rep.Completed {
		if strings.Contains(line, "job:daily-routine") {
			partialLine = line
		}
		if strings.Contains(line, "job:other") {
			normalLine = line
		}
	}
	if partialLine == "" || normalLine == "" {
		t.Fatalf("completed entries = %v", rep.Completed)
	}
	for _, want := range []string{"status=ok", "partial", "completed but incomplete", "some sites could not be completed"} {
		if !strings.Contains(partialLine, want) {
			t.Errorf("partial line %q missing %q", partialLine, want)
		}
	}
	for _, site := range sites {
		if !strings.Contains(partialLine, site) {
			t.Errorf("partial line %q missing incomplete site %q", partialLine, site)
		}
		if !strings.Contains(md, site) {
			t.Errorf("markdown missing incomplete site %q:\n%s", site, md)
		}
	}
	if strings.Contains(normalLine, "partial") {
		t.Errorf("non-partial line %q must not be marked partial", normalLine)
	}
	if rep.PartialCompleted != 1 {
		t.Errorf("PartialCompleted = %d, want 1", rep.PartialCompleted)
	}

	// JSON: partial must be machine-readable without parsing the text.
	b, err := rep.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"partial": true`) {
		t.Errorf("JSON output has no \"partial\": true:\n%s", b)
	}
	var decoded struct {
		Completed        []string        `json:"completed"`
		CompletedItems   []CompletedItem `json:"completed_items"`
		PartialCompleted int             `json:"partial_completed"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.PartialCompleted != 1 {
		t.Errorf("JSON partial_completed = %d, want 1", decoded.PartialCompleted)
	}
	if len(decoded.CompletedItems) != 2 {
		t.Fatalf("JSON completed_items = %d entries, want 2", len(decoded.CompletedItems))
	}
	var item *CompletedItem
	for i := range decoded.CompletedItems {
		if decoded.CompletedItems[i].Subject == "job:daily-routine" {
			item = &decoded.CompletedItems[i]
		}
		if decoded.CompletedItems[i].Subject == "job:other" && decoded.CompletedItems[i].Partial {
			t.Errorf("non-partial run decoded as partial: %+v", decoded.CompletedItems[i])
		}
	}
	if item == nil {
		t.Fatalf("JSON completed_items missing partial run: %+v", decoded.CompletedItems)
	}
	if !item.Partial {
		t.Errorf("partial run item.Partial = false: %+v", item)
	}
	if item.Status != "ok" {
		t.Errorf("partial run item.Status = %q, want ok", item.Status)
	}
	if len(item.SitesIncomplete) != len(sites) {
		t.Fatalf("item.SitesIncomplete = %v, want %v", item.SitesIncomplete, sites)
	}
	for i, site := range sites {
		if item.SitesIncomplete[i] != site {
			t.Errorf("item.SitesIncomplete[%d] = %q, want %q", i, item.SitesIncomplete[i], site)
		}
		if item.Incomplete[i] != site {
			t.Errorf("item.Incomplete[%d] = %q, want %q", i, item.Incomplete[i], site)
		}
	}
	if item.Note != "some sites could not be completed" {
		t.Errorf("item.Note = %q", item.Note)
	}
}
