package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/opsledger/internal/ledger"
)

func writeCfg(t *testing.T, ledgerPath string) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "opsledger.toml")
	content := "ledger_path = \"" + ledgerPath + "\"\ndb_path = \"" + filepath.Join(dir, "ledger.db") + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

// Normal transparency: wrapped command's exit code is returned verbatim.
func TestRunWrapperExitCodeTransparency(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, "ledger.jsonl")
	cfg := writeCfg(t, ledgerPath)
	if code := runMain([]string{"--config", cfg, "run", "--job", "okjob", "--", "sh", "-c", "exit 0"}); code != 0 {
		t.Fatalf("expected 0, got %d", code)
	}
	if code := runMain([]string{"--config", cfg, "run", "--job", "badjob", "--", "sh", "-c", "exit 7"}); code != 7 {
		t.Fatalf("expected 7, got %d", code)
	}
	events, err := ledger.ReadAll(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type == "run.finished" && e.DataString("status") == "failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("run.finished(failed) not recorded in normal case")
	}
}

// FAILURE ISOLATION (core guarantee): when recording is impossible — here the
// ledger path is a directory, so every append fails — the wrapped job must
// still run and its exit code must be returned verbatim. Recording failure
// must never turn a successful job into a failure, nor mask a real failure.
func TestRunWrapperLedgerFailureIsolation(t *testing.T) {
	dir := t.TempDir()
	brokenLedger := filepath.Join(dir, "ledger-as-dir")
	if err := os.Mkdir(brokenLedger, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := writeCfg(t, brokenLedger)
	// successful job stays successful despite broken ledger
	if code := runMain([]string{"--config", cfg, "run", "--job", "j", "--", "sh", "-c", "echo hello-from-job; exit 0"}); code != 0 {
		t.Fatalf("ledger failure changed success to %d", code)
	}
	// failing job keeps its own code (42), not a recording error code
	if code := runMain([]string{"--config", cfg, "run", "--job", "j", "--", "sh", "-c", "exit 42"}); code != 42 {
		t.Fatalf("ledger failure masked job exit code: got %d, want 42", code)
	}
}

// start/finish are best-effort: they exit 0 even when recording fails.
func TestStartFinishBestEffortOnLedgerFailure(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken")
	if err := os.Mkdir(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := writeCfg(t, broken)
	if code := runMain([]string{"--config", cfg, "start", "job:x"}); code != 0 {
		t.Fatalf("start must be best-effort, got %d", code)
	}
	if code := runMain([]string{"--config", cfg, "finish", "job:x", "--status", "ok"}); code != 0 {
		t.Fatalf("finish must be best-effort, got %d", code)
	}
}

// No control commands exist in the CLI surface.
func TestNoControlCommands(t *testing.T) {
	for _, c := range []string{"stop", "kill", "restart", "nudge", "rerun"} {
		if code := runMain([]string{c}); code == 0 {
			t.Fatalf("control command %q must not exist", c)
		}
	}
	_ = strings.TrimSpace
}
