// Package followx implements read-only log following.
// It NEVER opens target logs for writing: all opens use os.O_RDONLY.
package followx

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/nishimoto265/opsledger/internal/config"
	"github.com/nishimoto265/opsledger/internal/ledger"
)

// TransformJSON maps a JSON log line to an event using mapping:
// mapping maps envelope field (type/actor/subject/corr) -> JSON key in the line.
// Unmapped keys become data, minus excludeFields.
func TransformJSON(line string, t config.FollowTarget) (ledger.Event, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return ledger.Event{}, false
	}
	get := func(field string) string {
		k, ok := t.Mapping[field]
		if !ok {
			return ""
		}
		if v, ok := m[k].(string); ok {
			return v
		}
		return ""
	}
	typ := get("type")
	if typ == "" {
		if v, ok := t.Mapping["_type"]; ok {
			typ = v
		}
	}
	if typ == "" {
		typ = "activity.seen"
	}
	if !ledger.ValidTypes[typ] {
		typ = "activity.seen"
	}
	excluded := map[string]bool{}
	for _, f := range t.ExcludeFields {
		excluded[f] = true
	}
	data := map[string]any{"source": t.Name}
	for k, v := range m {
		if excluded[k] {
			continue
		}
		data[k] = v
	}
	e := ledger.NewEvent(typ, get("actor"), get("subject"), get("corr"), "follow:"+t.Name, data)
	// static overrides via mapping values prefixed with "=" e.g. actor="=agent:x"
	for field, v := range t.Mapping {
		if strings.HasPrefix(v, "=") {
			switch field {
			case "type":
				e.Type = strings.TrimPrefix(v, "=")
			case "actor":
				e.Actor = strings.TrimPrefix(v, "=")
			case "subject":
				e.Subject = strings.TrimPrefix(v, "=")
			}
		}
	}
	return e, true
}

// TransformRegex maps a line via named capture groups.
func TransformRegex(line string, t config.FollowTarget, re *regexp.Regexp) (ledger.Event, bool) {
	if re == nil {
		var err error
		re, err = regexp.Compile(t.Pattern)
		if err != nil {
			return ledger.Event{}, false
		}
	}
	m := re.FindStringSubmatch(line)
	if m == nil {
		return ledger.Event{}, false
	}
	names := re.SubexpNames()
	vals := map[string]string{}
	data := map[string]any{"source": t.Name}
	excluded := map[string]bool{}
	for _, f := range t.ExcludeFields {
		excluded[f] = true
	}
	for i, name := range names {
		if i == 0 || name == "" {
			continue
		}
		vals[name] = m[i]
		if name != "type" && name != "actor" && name != "subject" && name != "corr" && !excluded[name] {
			data[name] = m[i]
		}
	}
	typ := vals["type"]
	if typ == "" || !ledger.ValidTypes[typ] {
		typ = "activity.seen"
	}
	return ledger.NewEvent(typ, vals["actor"], vals["subject"], vals["corr"], "follow:"+t.Name, data), true
}

type offsetState struct {
	Offset int64  `json:"offset"`
	Inode  uint64 `json:"inode,omitempty"`
}

func loadOffset(path string) offsetState {
	b, err := os.ReadFile(path)
	if err != nil {
		return offsetState{}
	}
	var s offsetState
	_ = json.Unmarshal(b, &s)
	return s
}
func saveOffset(path string, s offsetState) error {
	b, _ := json.Marshal(s)
	return os.WriteFile(path, b, 0o644)
}

func inodeOf(fi os.FileInfo) uint64 {
	// portable fallback: use ModTime+Size hash-ish via Sys when available
	if sys, ok := fi.Sys().(interface{ Ino() uint64 }); ok {
		_ = sys
	}
	return uint64(fi.ModTime().UnixNano()) // only used as change hint alongside size check
}

// ProcessNew reads new lines from the target (read-only) starting at the
// persisted offset, converts them and appends events via appendFn.
// Handles rotation: if file shrank, restart from 0.
func ProcessNew(t config.FollowTarget, appendFn func(ledger.Event) error) (int, error) {
	offsetFile := t.OffsetFile
	if offsetFile == "" {
		offsetFile = t.Path + ".opsledger-offset.json"
	}
	st := loadOffset(offsetFile)
	// READ-ONLY open: O_RDONLY only. Never O_WRONLY/O_CREATE/O_TRUNC on target.
	f, err := os.OpenFile(t.Path, os.O_RDONLY, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if fi.Size() < st.Offset {
		st.Offset = 0 // rotation / truncation by the owner, not by us
	}
	if _, err := f.Seek(st.Offset, 0); err != nil {
		return 0, err
	}
	var re *regexp.Regexp
	if t.Adapter == "regex" {
		re, err = regexp.Compile(t.Pattern)
		if err != nil {
			return 0, fmt.Errorf("bad regex: %w", err)
		}
	}
	count := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	offset := st.Offset
	for sc.Scan() {
		line := sc.Text()
		offset += int64(len(line)) + 1
		var e ledger.Event
		var ok bool
		if t.Adapter == "regex" {
			e, ok = TransformRegex(line, t, re)
		} else {
			e, ok = TransformJSON(line, t)
		}
		if !ok {
			continue
		}
		if err := appendFn(e); err != nil {
			return count, err
		}
		count++
	}
	if err := sc.Err(); err != nil {
		return count, err
	}
	st.Offset = offset
	_ = inodeOf(fi)
	return count, saveOffset(offsetFile, st)
}

// Follow watches targets until done is closed, combining fsnotify with polling.
func Follow(targets []config.FollowTarget, appendFn func(ledger.Event) error, done <-chan struct{}, poll time.Duration) error {
	if poll <= 0 {
		poll = time.Second
	}
	watcher, _ := fsnotify.NewWatcher()
	if watcher != nil {
		defer watcher.Close()
		for _, t := range targets {
			_ = watcher.Add(t.Path)
		}
	}
	runAll := func() {
		for _, t := range targets {
			_, _ = ProcessNew(t, appendFn)
		}
	}
	runAll()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return nil
		case <-ticker.C:
			runAll()
		default:
			if watcher != nil {
				select {
				case <-watcher.Events:
					runAll()
				case <-done:
					return nil
				case <-time.After(200 * time.Millisecond):
				}
			} else {
				time.Sleep(200 * time.Millisecond)
			}
		}
	}
}
