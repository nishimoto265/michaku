package digest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nishimoto265/michaku/internal/ledger"
	"github.com/nishimoto265/michaku/internal/store"
)

// CompletedItem is the structured, machine-readable form of one entry in
// the Completed section. Completed holds the same entries as human-readable
// strings (used for Markdown); CompletedItems exposes the fields a JSON
// consumer needs — in particular Partial — without string parsing.
type CompletedItem struct {
	Subject         string    `json:"subject"`
	Type            string    `json:"type"`
	Status          string    `json:"status,omitempty"`
	Outcome         string    `json:"outcome,omitempty"`
	Partial         bool      `json:"partial"`
	Incomplete      []string  `json:"incomplete,omitempty"`
	SitesIncomplete []string  `json:"sites_incomplete,omitempty"`
	Note            string    `json:"note,omitempty"`
	At              time.Time `json:"at"`
	Text            string    `json:"text"`
}

type Report struct {
	Since            time.Time       `json:"since"`
	Until            time.Time       `json:"until"`
	Summary          map[string]int  `json:"summary"`
	Completed        []string        `json:"completed"`
	CompletedItems   []CompletedItem `json:"completed_items"`
	PartialCompleted int             `json:"partial_completed"`
	Findings         []string        `json:"findings"`
	Approvals        []string        `json:"approvals"`
	Unreported       []string        `json:"unreported"`
	Silent           []string        `json:"silent"`
	OpenFinding      int             `json:"open_findings"`
}

// stringSlice extracts a list of strings from a data value that may be a
// JSON array of strings or a single string.
func stringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return append([]string{}, t...)
	case []any:
		var out []string
		for _, el := range t {
			if s, ok := el.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if t != "" {
			return []string{t}
		}
	}
	return nil
}

// incompleteBreakdown returns the unfinished breakdown recorded on an
// event, if any. sites_incomplete is the canonical key (used for partial
// runs whose remaining work is a list of sites); a few generic aliases and
// any other "*incomplete*" list key are accepted as well. The second
// return value reports whether the breakdown came from sites_incomplete.
func incompleteBreakdown(e ledger.Event) ([]string, bool) {
	m := e.DataMap()
	for _, k := range []string{"sites_incomplete", "incomplete", "incomplete_sites", "incomplete_items", "missing_sites", "missing", "unfinished"} {
		if v, ok := m[k]; ok {
			if items := stringSlice(v); len(items) > 0 {
				return items, k == "sites_incomplete"
			}
		}
	}
	var keys []string
	for k := range m {
		if strings.Contains(strings.ToLower(k), "incomplete") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if items := stringSlice(m[k]); len(items) > 0 {
			return items, false
		}
	}
	return nil, false
}

func Build(st *store.State, since, until time.Time) Report {
	r := Report{Since: since, Until: until, Summary: map[string]int{}, OpenFinding: len(st.Findings)}
	for _, e := range st.Events {
		r.Summary[e.Type]++
		if e.TS.Before(since) || e.TS.After(until) {
			continue
		}
		switch e.Type {
		case "run.finished":
			status := e.DataString("status")
			partial, _ := e.DataBool("partial")
			incomplete, isSites := incompleteBreakdown(e)
			note := e.DataString("note")
			text := fmt.Sprintf("%s %s status=%s", e.Subject, e.Type, status)
			if partial {
				text += " (partial: completed but incomplete)"
			}
			if len(incomplete) > 0 {
				text += "; incomplete: " + strings.Join(incomplete, ", ")
			}
			if note != "" {
				text += "; note: " + note
			}
			text += " at " + e.TS.Format(time.RFC3339)
			item := CompletedItem{Subject: e.Subject, Type: e.Type, Status: status, Partial: partial, Note: note, At: e.TS, Text: text}
			if len(incomplete) > 0 {
				item.Incomplete = incomplete
				if isSites {
					item.SitesIncomplete = incomplete
				}
			}
			if partial {
				r.PartialCompleted++
			}
			r.Completed = append(r.Completed, text)
			r.CompletedItems = append(r.CompletedItems, item)
		case "delegation.reported":
			outcome := e.DataString("outcome")
			partial := outcome == "partial"
			if v, ok := e.DataBool("partial"); ok && v {
				partial = true
			}
			incomplete, isSites := incompleteBreakdown(e)
			note := e.DataString("note")
			text := fmt.Sprintf("%s reported outcome=%s", e.Subject, outcome)
			if partial && outcome != "partial" {
				text += " (partial: completed but incomplete)"
			}
			if len(incomplete) > 0 {
				text += "; incomplete: " + strings.Join(incomplete, ", ")
			}
			if note != "" {
				text += "; note: " + note
			}
			text += " at " + e.TS.Format(time.RFC3339)
			item := CompletedItem{Subject: e.Subject, Type: e.Type, Outcome: outcome, Partial: partial, Note: note, At: e.TS, Text: text}
			if len(incomplete) > 0 {
				item.Incomplete = incomplete
				if isSites {
					item.SitesIncomplete = incomplete
				}
			}
			if partial {
				r.PartialCompleted++
			}
			r.Completed = append(r.Completed, text)
			r.CompletedItems = append(r.CompletedItems, item)
		}
	}
	var fkeys []string
	for k := range st.Findings {
		fkeys = append(fkeys, k)
	}
	sort.Strings(fkeys)
	for _, k := range fkeys {
		f := st.Findings[k]
		r.Findings = append(r.Findings, fmt.Sprintf("[%s] %s on %s since %s — %s", f.Severity, f.Rule, f.Subject, f.Raised.Format(time.RFC3339), f.Detail))
	}
	var akeys []string
	for k := range st.Approvals {
		akeys = append(akeys, k)
	}
	sort.Strings(akeys)
	for _, k := range akeys {
		a := st.Approvals[k]
		r.Approvals = append(r.Approvals, fmt.Sprintf("%s (%s) pending %s — %s", a.Subject, a.Key, until.Sub(a.Requested).Round(time.Minute), a.What))
	}
	var dkeys []string
	for k := range st.Delegations {
		dkeys = append(dkeys, k)
	}
	sort.Strings(dkeys)
	for _, k := range dkeys {
		d := st.Delegations[k]
		due := "no due"
		if d.Due != nil {
			due = "due " + d.Due.Format(time.RFC3339)
		}
		r.Unreported = append(r.Unreported, fmt.Sprintf("%s (%s) created %s, %s — %s", d.Subject, d.Key, d.Created.Format(time.RFC3339), due, d.Task))
	}
	var actors []string
	for a := range st.Actors {
		actors = append(actors, a)
	}
	sort.Strings(actors)
	for _, a := range actors {
		last := st.Actors[a]
		if until.Sub(last) > time.Hour {
			r.Silent = append(r.Silent, fmt.Sprintf("%s last seen %s (%s ago)", a, last.Format(time.RFC3339), until.Sub(last).Round(time.Minute)))
		}
	}
	return r
}

func section(b *strings.Builder, title string, items []string) {
	b.WriteString("## " + title + "\n\n")
	if len(items) == 0 {
		b.WriteString("- (none)\n\n")
		return
	}
	for _, it := range items {
		b.WriteString("- " + it + "\n")
	}
	b.WriteString("\n")
}

func (r Report) Markdown() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# michaku digest\n\nPeriod: %s – %s\n\n", r.Since.Format(time.RFC3339), r.Until.Format(time.RFC3339)))
	b.WriteString("## Summary\n\n")
	b.WriteString(fmt.Sprintf("- Events in ledger: %d types tracked\n- Open findings: %d\n- Completed items in period: %d\n\n", len(r.Summary), r.OpenFinding, len(r.Completed)))
	section(&b, "Completed", r.Completed)
	section(&b, "Stalled / Abnormal", r.Findings)
	section(&b, "Pending Approvals", r.Approvals)
	combined := append(append([]string{}, r.Unreported...), r.Silent...)
	section(&b, "Unreported Delegations & Silent Actors", combined)
	return b.String()
}

func (r Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }
