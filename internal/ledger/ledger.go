package ledger

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/oklog/ulid/v2"
)

// Event is the envelope for every ledger entry (proposal §1.2).
type Event struct {
	V       int             `json:"v"`
	ID      string          `json:"id"`
	TS      time.Time       `json:"ts"`
	Type    string          `json:"type"`
	Actor   string          `json:"actor,omitempty"`
	Subject string          `json:"subject,omitempty"`
	Corr    string          `json:"corr,omitempty"`
	Src     string          `json:"src,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

var ValidTypes = map[string]bool{
	"actor.registered": true, "actor.heartbeat": true, "activity.seen": true,
	"schedule.declared": true, "run.started": true, "run.finished": true,
	"delegation.created": true, "delegation.reported": true, "delegation.cancelled": true,
	"approval.requested": true, "approval.resolved": true,
	"finding.raised": true, "finding.resolved": true,
}

func NewID() string { return ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String() }

func NewEvent(typ, actor, subject, corr, src string, data any) Event {
	var raw json.RawMessage
	if data != nil {
		b, _ := json.Marshal(data)
		raw = b
	}
	return Event{V: 1, ID: NewID(), TS: time.Now(), Type: typ, Actor: actor, Subject: subject, Corr: corr, Src: src, Data: raw}
}

func (e Event) DataMap() map[string]any {
	if len(e.Data) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	_ = json.Unmarshal(e.Data, &m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}
func (e Event) DataString(k string) string {
	m := e.DataMap()
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
func (e Event) DataTime(k string) (time.Time, bool) {
	s := e.DataString(k)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

func Validate(e Event) error {
	if e.V != 1 {
		return fmt.Errorf("unsupported v=%d", e.V)
	}
	if e.ID == "" {
		return fmt.Errorf("missing id")
	}
	if e.TS.IsZero() {
		return fmt.Errorf("missing ts")
	}
	if !ValidTypes[e.Type] {
		return fmt.Errorf("unknown type %q", e.Type)
	}
	return nil
}

// Append writes one event as a single line under an exclusive flock.
// The write is a single Write call so concurrent appenders do not interleave.
func Append(path string, e Event) error {
	if err := Validate(e); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if _, err := f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}

// AppendBestEffort appends but only warns on failure. This is the failure-
// isolation primitive: recording must never break the observed process.
func AppendBestEffort(path string, e Event, warn func(string)) {
	if err := Append(path, e); err != nil {
		if warn != nil {
			warn(fmt.Sprintf("opsledger: warning: failed to record %s: %v", e.Type, err))
		}
	}
}

func ReadAll(path string) ([]Event, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(s), &e); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if err := Validate(e); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// ValidateLine validates one raw JSONL line (used by import).
func ValidateLine(s string) (Event, error) {
	var e Event
	if err := json.Unmarshal([]byte(s), &e); err != nil {
		return e, err
	}
	return e, Validate(e)
}
