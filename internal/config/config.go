package config

import (
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

type FollowTarget struct {
	Name          string            `toml:"name"`
	Path          string            `toml:"path"`
	Adapter       string            `toml:"adapter"` // json | regex
	Pattern       string            `toml:"pattern"`
	Mapping       map[string]string `toml:"mapping"`
	ExcludeFields []string          `toml:"exclude_fields"`
	OffsetFile    string            `toml:"offset_file"`
}

type ScheduleExpect struct {
	Subject string `toml:"subject"`
	Expr    string `toml:"expr"`
	Every   string `toml:"every"`
	Grace   string `toml:"grace"`
}

type Rule struct {
	Enabled   bool   `toml:"enabled"`
	Grace     string `toml:"grace"`
	Threshold string `toml:"threshold"`
	Reminder  string `toml:"reminder"`
	// report_missing
	CriticalAfter string `toml:"critical_after"`
	// missing_run
	MaxDuration string `toml:"max_duration"`
	// repeated_failure
	Window string `toml:"window"`
	Count  int    `toml:"count"`
}

func (r Rule) Dur(field string, def time.Duration) time.Duration {
	var s string
	switch field {
	case "grace":
		s = r.Grace
	case "threshold":
		s = r.Threshold
	case "reminder":
		s = r.Reminder
	case "critical_after":
		s = r.CriticalAfter
	case "max_duration":
		s = r.MaxDuration
	case "window":
		s = r.Window
	}
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

type NotifyTarget struct {
	Type   string `toml:"type"` // webhook | file
	URL    string `toml:"url"`
	Format string `toml:"format"` // generic | slack | discord | ntfy
	Path   string `toml:"path"`
}

type Config struct {
	LedgerPath        string           `toml:"ledger_path"`
	DBPath            string           `toml:"db_path"`
	CaptureOutputTail bool             `toml:"capture_output_tail"`
	OutputTailLines   int              `toml:"output_tail_lines"`
	Follow            []FollowTarget   `toml:"follow"`
	Schedules         []ScheduleExpect `toml:"schedules"`
	Rules             map[string]Rule  `toml:"rules"`
	Notify            []NotifyTarget   `toml:"notify"`
}

func Default() *Config {
	return &Config{
		LedgerPath: "ledger.jsonl", DBPath: "ledger.db", OutputTailLines: 20,
		Rules: map[string]Rule{
			"missing_run":      {Enabled: true, Grace: "5m", MaxDuration: "2h", Reminder: "1h"},
			"report_missing":   {Enabled: true, Grace: "0s", CriticalAfter: "24h", Reminder: "6h"},
			"approval_stale":   {Enabled: true, Threshold: "24h", Reminder: "24h"},
			"heartbeat_stale":  {Enabled: true, Threshold: "1h", Reminder: "1h"},
			"repeated_failure": {Enabled: true, Window: "24h", Count: 3, Reminder: "6h"},
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if cfg.Rules == nil {
		cfg.Rules = Default().Rules
	}
	// Fill defaults for rules not present.
	for k, v := range Default().Rules {
		if _, ok := cfg.Rules[k]; !ok {
			cfg.Rules[k] = v
		}
	}
	if cfg.OutputTailLines == 0 {
		cfg.OutputTailLines = 20
	}
	return cfg, nil
}

func (c *Config) Rule(name string) Rule {
	if r, ok := c.Rules[name]; ok {
		return r
	}
	return Rule{Enabled: true}
}

const Template = `# opsledger configuration
ledger_path = "ledger.jsonl"
db_path = "ledger.db"
capture_output_tail = false
output_tail_lines = 20

# [[follow]]
# name = "cron-log"
# path = "/var/log/my-agent.log"
# adapter = "json"          # json | regex
# mapping = { type = "event", actor = "actor", subject = "job" }
# exclude_fields = ["secret", "token"]

# [[schedules]]
# subject = "job:daily"
# expr = "20 9 * * *"
# grace = "10m"

[rules.missing_run]
enabled = true
grace = "5m"
max_duration = "2h"
reminder = "1h"

[rules.report_missing]
enabled = true
grace = "0s"
critical_after = "24h"
reminder = "6h"

[rules.approval_stale]
enabled = true
threshold = "24h"
reminder = "24h"

[rules.heartbeat_stale]
enabled = true
threshold = "1h"
reminder = "1h"

[rules.repeated_failure]
enabled = true
window = "24h"
count = 3
reminder = "6h"

# [[notify]]
# type = "webhook"
# url = "https://hooks.example.com/..."
# format = "generic"        # generic | slack | discord | ntfy
#
# [[notify]]
# type = "file"
# path = "alerts.log"
`
