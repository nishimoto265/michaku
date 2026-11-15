package digest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nishimoto265/opsledger/internal/store"
)

type Report struct {
	Since       time.Time      `json:"since"`
	Until       time.Time      `json:"until"`
	Summary     map[string]int `json:"summary"`
	Completed   []string       `json:"completed"`
	Findings    []string       `json:"findings"`
	Approvals   []string       `json:"approvals"`
	Unreported  []string       `json:"unreported"`
	Silent      []string       `json:"silent"`
	OpenFinding int            `json:"open_findings"`
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
			r.Completed = append(r.Completed, fmt.Sprintf("%s %s status=%s at %s", e.Subject, e.Type, e.DataString("status"), e.TS.Format(time.RFC3339)))
		case "delegation.reported":
			r.Completed = append(r.Completed, fmt.Sprintf("%s reported outcome=%s at %s", e.Subject, e.DataString("outcome"), e.TS.Format(time.RFC3339)))
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
	b.WriteString(fmt.Sprintf("# opsledger digest\n\nPeriod: %s – %s\n\n", r.Since.Format(time.RFC3339), r.Until.Format(time.RFC3339)))
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
