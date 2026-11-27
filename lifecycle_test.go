package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSessionLifecycle(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{SessionsDir: dir, Profile: "muse"}
	archive := syncExportPath(dir, "old")
	put(t, archive, "historical")
	observed := map[string]telemetrySession{}
	for i, id := range []string{"old", "foreign", "valid"} {
		observed[id] = telemetrySession{ID: id, Count: 4, Last: time.Unix(int64(3-i), 0)}
	}
	put(t, syncStatePath(cfg), `{"counts":{"old":2}}`)
	calls := []string{}
	export := func(cfg Config, id, path string) (bool, error) {
		calls = append(calls, id)
		if id == "valid" || (cfg.Profile == "other" && id == "foreign") {
			put(t, path, `{"id":"`+id+`"}`)
			return true, nil
		}
		return false, nil
	}
	run := func() syncState {
		t.Helper()
		state, exists, err := readSyncState(syncStatePath(cfg))
		if err != nil {
			t.Fatal(err)
		}
		if err := incrementalSync(cfg, observed, state, exists, export); err != nil {
			t.Fatal(err)
		}
		state, _, err = readSyncState(syncStatePath(cfg))
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	state := run()
	if strings.Join(calls, ",") != "old,foreign,valid" || state.Counts["old"] != 2 || state.Counts["valid"] != 4 || state.Availability["old"].Observed != 4 || !state.Availability["foreign"].Missing {
		t.Fatal(calls, state)
	}
	calls = nil
	run()
	if len(calls) != 0 {
		t.Fatal(calls)
	}
	observed["old"] = telemetrySession{ID: "old", Count: 5}
	run()
	if strings.Join(calls, ",") != "old" {
		t.Fatal(calls)
	}
	cfg.Profile = "other"
	state = run()
	if state.Availability["foreign"].Missing || state.Counts["foreign"] != 4 {
		t.Fatal(state)
	}
	data, err := os.ReadFile(archive)
	if err != nil || string(data) != "historical" {
		t.Fatal(string(data), err)
	}
}

func TestHermesExportFailureClassification(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	path := filepath.Join(dir, "archive.jsonl")
	put(t, path, "old archive")
	for _, tc := range []struct {
		msg     string
		code    int
		missing bool
	}{
		{"No session 'abc'. Run: hermes sessions list to find the id.", 1, true},
		{"No session 'other'. Run: hermes sessions list to find the id.", 1, false},
		{"store locked", 1, false}, {"invalid output", 0, false},
	} {
		if runtime.GOOS == "windows" {
			put(t, filepath.Join(dir, "hermes.cmd"), "@echo off\r\necho "+tc.msg+"\r\nexit /b "+string(rune('0'+tc.code))+"\r\n")
		} else {
			script := filepath.Join(dir, "hermes")
			put(t, script, "#!/bin/sh\nprintf '%s\\n' \""+tc.msg+"\"\nexit "+string(rune('0'+tc.code))+"\n")
			if err := os.Chmod(script, 0755); err != nil {
				t.Fatal(err)
			}
		}
		ok, err := exportHermesSession(Config{}, "abc", path)
		if ok || (err == nil) != tc.missing {
			t.Fatalf("%q: %v %v", tc.msg, ok, err)
		}
		data, _ := os.ReadFile(path)
		if string(data) != "old archive" {
			t.Fatal("archive changed")
		}
	}
}

func TestNonToolTelemetry(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{SessionsDir: dir, TelemetryFile: filepath.Join(dir, "events.log"), SessionPattern: "*.jsonl"}
	put(t, cfg.TelemetryFile, `{"event":"api.completed","session_id":"hello"}`)
	put(t, filepath.Join(dir, "exports.jsonl"), "{\"id\":\"hello\"}\n{\"id\":\"silent\"}")
	rows, _, err := discoverSessions(cfg)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	for _, r := range rows {
		want := 0
		if r.ID == "hello" {
			want = 1
		}
		if r.TelemetryEvents != want || r.TelemetryToolEvents != 0 || r.TelemetryTools != 0 {
			t.Fatal(r)
		}
	}
}
func TestUnavailableRecoveryAndTransientFailure(t *testing.T) {
	cfg := Config{SessionsDir: t.TempDir(), Profile: "experiment"}
	observed := map[string]telemetrySession{"session": {ID: "session", Count: 9}}
	for _, mode := range []string{"expired", "explicit"} {
		check := availability{Missing: true, Observed: 9, Scope: syncScope(cfg), Checked: time.Now()}
		cfg.recheck = mode == "explicit"
		if !cfg.recheck {
			check.Checked = check.Checked.Add(-25 * time.Hour)
		}
		initial := syncState{Counts: map[string]int{"session": 3}, Availability: map[string]availability{"session": check}}
		if err := saveSyncState(syncStatePath(cfg), initial); err != nil {
			t.Fatal(err)
		}
		attempts := 0
		export := func(Config, string, string) (bool, error) {
			attempts++
			if attempts == 1 {
				return false, os.ErrPermission
			}
			return true, nil
		}
		if err := incrementalSync(cfg, observed, initial, true, export); err == nil {
			t.Fatal("transient failure suppressed")
		}
		state, _, err := readSyncState(syncStatePath(cfg))
		if err != nil || state.Counts["session"] != 3 {
			t.Fatal(state, err)
		}
		if err := incrementalSync(cfg, observed, state, true, export); err != nil {
			t.Fatal(err)
		}
		state, _, err = readSyncState(syncStatePath(cfg))
		if err != nil || state.Counts["session"] != 9 || state.Availability["session"].Missing || attempts != 2 {
			t.Fatal(state, err, attempts)
		}
	}
	if args := hermesExport(cfg, "sessions", "export").Args; strings.Join(args, " ") != "hermes --profile experiment sessions export" {
		t.Fatal(args)
	}
}

func TestFullSyncKeepsPreviousSessions(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{SessionsDir: filepath.Join(dir, "sessions"), SessionPattern: "*.jsonl"}
	old := `{"id":"old","title":"archive"}`
	put(t, filepath.Join(cfg.SessionsDir, "hermes-bench-synced.jsonl"), old)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if runtime.GOOS == "windows" {
		put(t, filepath.Join(dir, "hermes.cmd"), "@echo off\r\n> \"%~3\" echo {\"id\":\"live\"}\r\n")
	} else {
		script := filepath.Join(dir, "hermes")
		put(t, script, "#!/bin/sh\nprintf '%s\\n' '{\"id\":\"live\"}' > \"$3\"\n")
		if err := os.Chmod(script, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := syncSessions(cfg, true); err != nil {
		t.Fatal(err)
	}
	candidates, _, err := discoverSessions(cfg)
	if err != nil || len(candidates) != 2 {
		t.Fatal(candidates, err)
	}
	paths, _ := filepath.Glob(filepath.Join(cfg.SessionsDir, "hermes-bench-history-*.jsonl"))
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil || string(data) != old {
		t.Fatal(string(data), err)
	}
}
