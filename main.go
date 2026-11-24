package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Config struct {
	SessionsDir    string `json:"sessions_dir"`
	TelemetryFile  string `json:"telemetry_file"`
	ReportsDir     string `json:"reports_dir"`
	SessionPattern string `json:"session_pattern"`
	Recursive      bool   `json:"recursive"`
}

type SessionCandidate struct {
	Path              string
	FileModTime       time.Time
	ID                string
	Title             string
	Model             string
	Provider          string
	Source            string
	CWD               string
	Started           *time.Time
	Ended             *time.Time
	Messages          int
	ActiveMessages    int
	ArchivedMessages  int
	ToolCalls         int
	ActiveToolCalls   int
	ArchivedToolCalls int
	InputTokens       *int64
	OutputTokens      *int64
	TelemetryEvents   int
	TelemetryTools    int
	Raw               map[string]any
}

type ToolStat struct {
	Calls       int
	TotalMS     float64
	MaxMS       float64
	ResultBytes int64
	ResultChars int64
	Failures    int
	Unknown     int
}

type APIStat struct {
	Started      int
	Completed    int
	Errors       int
	TotalSeconds float64
	Providers    map[string]int
	Models       map[string]int
}

type StreamStat struct {
	Started                int
	Ended                  int
	WithReasoning          int
	WithText               int
	MissingEnd             int
	TotalObservedMS        float64
	MaxGapMS               float64
	TextDeltas             int
	ReasoningDeltas        int
	FirstTextMS            []float64
	FirstReasoningMS       []float64
	ObservedReasoningSpans []float64
	ObservedTextSpans      []float64
	Records                []StreamRow
}

type StreamRow struct {
	DurationMS      float64
	FirstReasoning  *float64
	LastReasoning   *float64
	FirstText       *float64
	LastText        *float64
	TextDeltas      int
	ReasoningDeltas int
	MaxGapMS        float64
}

// TranscriptMessage is one persisted Hermes message. Text is rendered only to the
// local Markdown report, never to the concise terminal summary.
type TranscriptMessage struct {
	Index                   int
	Role                    string
	Active                  bool
	Compacted               bool
	Timestamp               *time.Time
	Content                 string
	Reasoning               string
	ReasoningSource         string
	ReasoningSidecarPresent bool
	ToolName                string
	ToolCalls               []string
	TokenCount              *int64
	FinishReason            string
}

type APIRow struct {
	RequestID        string
	Model            string
	Provider         string
	DurationSeconds  *float64
	FinishReason     string
	PromptTokens     *int64
	CompletionTokens *int64
	CacheReadTokens  *int64
	CacheWriteTokens *int64
	ReasoningTokens  *int64
	TotalTokens      *int64
}

type Gap struct {
	FromRole string
	ToRole   string
	GapMS    float64
}

type Event struct {
	TS        time.Time
	Name      string
	Tool      string
	SessionID string
	TaskID    string
	CallID    string
	Duration  float64
	Bytes     int64
	Chars     int64
	Status    string
	ErrorType string
	Raw       map[string]any
}

type Interval struct{ Start, End time.Time }

type Analysis struct {
	SessionID   string
	Title       string
	Model       string
	Provider    string
	Source      string
	CWD         string
	SessionPath string
	Started     *time.Time
	Ended       *time.Time
	WallMS      *float64

	Messages          int
	ActiveMessages    int
	ArchivedMessages  int
	ToolCalls         int
	ActiveToolCalls   int
	ArchivedToolCalls int
	ToolNames         map[string]int
	InputTokens       *int64
	OutputTokens      *int64
	CacheReadTokens   *int64
	CacheWriteTokens  *int64
	ReasoningTokens   *int64
	CostUSD           *float64
	LargestGapMS      *float64
	TopGaps           []Gap
	TimingComplete    *bool

	TelemetryEvents          int
	API                      APIStat
	Stream                   StreamStat
	SessionUserMessages      int
	TelemetryBadLines        int
	TelemetryUnattributed    int
	TelemetryStarted         int
	TelemetryCompleted       int
	TelemetryToolMS          float64
	TelemetryToolUnionMS     float64
	TelemetryResultBytes     int64
	TelemetryResultChars     int64
	TelemetryFailures        int
	TelemetryUnknownOutcomes int
	ToolStats                map[string]*ToolStat
	TurnCount                int
	TurnDurationTotalMS      float64
	TurnDurationMaxMS        float64
	TelemetryFirst           *time.Time
	TelemetryLast            *time.Time
	Warnings                 []string
	SessionSHA256            string
	Transcript               []TranscriptMessage
	APIUsageRows             []APIRow
	TelemetrySHA256          string
	FirstAPIStart            *time.Time
	FirstTurnStart           *time.Time
	TurnWindows              []Interval
	ToolWindows              []Interval
	TextWindows              []Interval
}

func main() {
	args, configOverride := extractConfigArg(os.Args[1:])
	if len(args) == 0 {
		args = []string{"list"}
	}
	command := args[0]
	args = args[1:]

	configPath, err := resolveConfigPath(configOverride)
	if err != nil {
		fatal(err)
	}

	switch command {
	case "init":
		if err := initializeConfig(configPath); err != nil {
			fatal(err)
		}
	case "sync":
		cfg, err := loadConfig(configPath)
		if err != nil {
			fatal(err)
		}
		full := false
		for _, arg := range args {
			if arg == "--all" {
				full = true
			} else {
				fatal(fmt.Errorf("unknown sync option: %s", arg))
			}
		}
		if err := syncSessions(cfg, full); err != nil {
			fatal(err)
		}
	case "run":
		if len(args) != 0 {
			fatal(errors.New("usage: hermes-bench run"))
		}
		cfg, err := loadConfig(configPath)
		if err != nil {
			fatal(err)
		}
		if err := syncSessions(cfg, false); err != nil {
			fatal(err)
		}
		if err := reportLatest(cfg); err != nil {
			fatal(err)
		}
	case "list":
		cfg, err := loadConfig(configPath)
		if err != nil {
			fatal(err)
		}
		if err := listSessions(cfg); err != nil {
			fatal(err)
		}
	case "report":
		if len(args) == 0 {
			fatal(errors.New("usage: hermes-bench report <index> [--markdown]"))
		}
		index := 0
		if !strings.EqualFold(args[0], "latest") {
			parsed, err := strconv.Atoi(args[0])
			if err != nil || parsed < 1 {
				fatal(fmt.Errorf("invalid session index %q; use 'latest' or a number shown by 'hermes-bench list'", args[0]))
			}
			index = parsed
		}
		markdown := true
		for _, arg := range args[1:] {
			if arg == "--markdown" || arg == "-m" {
				markdown = true
			} else {
				fatal(fmt.Errorf("unknown report option: %s", arg))
			}
		}
		cfg, err := loadConfig(configPath)
		if err != nil {
			fatal(err)
		}
		if index == 0 {
			index = 1
		}
		if err := reportSession(cfg, index, markdown); err != nil {
			fatal(err)
		}
	case "help", "-h", "--help":
		printUsage()
	default:
		fatal(fmt.Errorf("unknown command %q", command))
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(1)
}

func printUsage() {
	fmt.Println(`Hermes Bench — local session report generator

Usage:
  hermes-bench                  List available sessions (default)
  hermes-bench list             List available sessions
  hermes-bench report <index>   Print terminal report and save Markdown in ./reports
  hermes-bench report <index> --markdown
                                Save the full Markdown report (default for report)
  hermes-bench report latest    Report most recent exported session
  hermes-bench sync             Incrementally export new/changed telemetry sessions
  hermes-bench sync --all       Export all stored Hermes sessions (slower)
  hermes-bench run              Incremental sync + report latest, in one command
  hermes-bench init             Create the separate configuration file

Options:
  --config <path>               Use a specific configuration file (can appear anywhere)
  -m, --markdown                Save the full Markdown report

The list index is selected from the latest session listing. Sessions and telemetry
are paired by exact Hermes session ID, not by title or filename.`)
}

func extractConfigArg(args []string) ([]string, string) {
	out := make([]string, 0, len(args))
	config := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" {
			if i+1 >= len(args) {
				fatal(errors.New("--config requires a path"))
			}
			config = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--config=") {
			config = strings.TrimPrefix(args[i], "--config=")
			continue
		}
		out = append(out, args[i])
	}
	return out, config
}

func defaultConfigPath() string {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return filepath.Join(v, "hermes-bench", "config.json")
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "hermes-bench", "config.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "hermes-bench", "config.json")
}

func resolveConfigPath(override string) (string, error) {
	if override == "" {
		return defaultConfigPath(), nil
	}
	p := expandPath(override)
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func defaultConfig() Config {
	base := ""
	if os.Getenv("HERMES_HOME") != "" {
		base = expandPath(os.Getenv("HERMES_HOME"))
	} else if os.Getenv("LOCALAPPDATA") != "" {
		base = filepath.Join(os.Getenv("LOCALAPPDATA"), "hermes")
	} else {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".hermes")
	}
	return Config{
		SessionsDir:    filepath.Join(base, "bench", "sessions"),
		TelemetryFile:  filepath.Join(base, "experiments", "events.jsonl"),
		ReportsDir:     defaultReportsDir(),
		SessionPattern: "*.jsonl",
		Recursive:      false,
	}
}

// Compiled binaries save reports beside the executable. With `go run .`, the
// executable is temporary, so keep reports in the project working directory.
func defaultReportsDir() string {
	exe, err := os.Executable()
	if err == nil {
		parent := filepath.Dir(exe)
		if !strings.Contains(strings.ToLower(parent), "go-build") {
			return filepath.Join(parent, "reports")
		}
	}
	return "reports"
}

// Sync only sessions with new telemetry since the previous successful snapshot.
// Hermes is the source of truth for exports; this tool never edits state.db.
// The initial incremental sync bootstraps old IDs from the existing exports and
// exports the latest observed session, avoiding a costly historical replay.
type syncState struct {
	Counts map[string]int `json:"counts"`
}

type telemetrySession struct {
	ID    string
	Count int
	Last  time.Time
}

func collectTelemetrySessions(path string) (map[string]telemetrySession, error) {
	out := map[string]telemetrySession{}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return out, nil
	} else if err != nil {
		return nil, err
	}
	items, bad, err := readJSONLObjects(path)
	if err != nil {
		return nil, err
	}
	if bad > 0 {
		return nil, fmt.Errorf("telemetry has %d malformed JSONL lines; refusing partial incremental sync", bad)
	}
	for _, item := range items {
		id := getString(item, "session_id")
		if id == "" {
			continue
		}
		v := out[id]
		v.ID = id
		v.Count++
		if ts, ok := parseTime(first(item, "ts")); ok && ts.After(v.Last) {
			v.Last = ts
		}
		out[id] = v
	}
	return out, nil
}

func syncStatePath(cfg Config) string {
	return filepath.Join(cfg.SessionsDir, ".hermes-bench-sync.json")
}

func readSyncState(path string) (syncState, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return syncState{Counts: map[string]int{}}, false, nil
	}
	if err != nil {
		return syncState{}, false, err
	}
	var state syncState
	if err := json.Unmarshal(data, &state); err != nil {
		return syncState{}, false, err
	}
	if state.Counts == nil {
		state.Counts = map[string]int{}
	}
	return state, true, nil
}

func saveSyncState(path string, state syncState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func syncExportPath(dir, id string) string {
	hash := sha256.Sum256([]byte(id))
	return filepath.Join(dir, "hermes-bench-session-"+hex.EncodeToString(hash[:12])+".jsonl")
}

func exportHermesSession(id, path string) (bool, error) {
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	cmd := exec.Command("hermes", "sessions", "export", tmp, "--session-id", id, "--redact")
	result, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("Hermes export %s failed: %w: %s", shortID(id), err, strings.TrimSpace(string(result)))
	}
	objects, bad, readErr := readJSONLObjects(tmp)
	if readErr != nil || bad > 0 || len(objects) != 1 || getString(unwrapSession(objects[0]), "id", "session_id", "sessionId") != id {
		_ = os.Remove(tmp)
		// Missing/deleted Hermes sessions can return success with no export.
		return false, nil
	}
	// Windows os.Rename will not replace an existing destination.
	// Retain the previous export if new data could not be validated.
	if _, err := os.Stat(path); err == nil {
		backup := path + ".bak"
		_ = os.Remove(backup)
		if err := os.Rename(path, backup); err != nil {
			_ = os.Remove(tmp)
			return false, err
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Rename(backup, path)
			return false, err
		}
		_ = os.Remove(backup)
	} else if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	return true, nil
}

func reportLatest(cfg Config) error { return reportSession(cfg, 1, true) }

func syncSessions(cfg Config, full bool) error {
	if err := os.MkdirAll(cfg.SessionsDir, 0755); err != nil {
		return err
	}
	if full {
		out := filepath.Join(cfg.SessionsDir, "hermes-bench-synced.jsonl")
		tmp := out + ".tmp"
		cmd := exec.Command("hermes", "sessions", "export", tmp, "--redact")
		result, err := cmd.CombinedOutput()
		if err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("Hermes full export failed: %w\n%s", err, strings.TrimSpace(string(result)))
		}
		if _, err := os.Stat(out); err == nil {
			backup := out + ".bak"
			_ = os.Remove(backup)
			if err := os.Rename(out, backup); err != nil {
				return err
			}
			if err := os.Rename(tmp, out); err != nil {
				_ = os.Rename(backup, out)
				return err
			}
			_ = os.Remove(backup)
		} else if err := os.Rename(tmp, out); err != nil {
			return err
		}
		fmt.Println("Full Hermes export:", out)
		if len(result) > 0 {
			fmt.Println(strings.TrimSpace(string(result)))
		}
		// Do not alter the incremental cursor: some sessions may still change.
		return nil
	}
	observed, err := collectTelemetrySessions(cfg.TelemetryFile)
	if err != nil {
		return err
	}
	if len(observed) == 0 {
		return errors.New("no session-attributed telemetry found; run a Hermes task or use sync --all")
	}
	state, exists, err := readSyncState(syncStatePath(cfg))
	if err != nil {
		return fmt.Errorf("read sync state: %w", err)
	}
	ordered := make([]telemetrySession, 0, len(observed))
	for _, session := range observed {
		ordered = append(ordered, session)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Last.After(ordered[j].Last) })
	if !exists {
		// Adopt the historical log as baseline; only the newest run must be
		// exported now. Use sync --all to import all historical sessions.
		for _, session := range ordered {
			state.Counts[session.ID] = session.Count
		}
		fmt.Println("Initialized incremental cursor from existing telemetry; historical sessions unchanged (sync --all imports them).")
	}
	updated, missing := 0, 0
	for _, session := range ordered {
		path := syncExportPath(cfg.SessionsDir, session.ID)
		if exists && state.Counts[session.ID] == session.Count {
			continue
		}
		if !exists && updated > 0 {
			break
		}
		ok, err := exportHermesSession(session.ID, path)
		if err != nil {
			return err
		}
		if ok {
			updated++
			fmt.Println("Synced:", shortID(session.ID))
		} else {
			missing++
			fmt.Println("Skipped absent/deleted session:", shortID(session.ID))
		}
		state.Counts[session.ID] = session.Count
		if err := saveSyncState(syncStatePath(cfg), state); err != nil {
			return err
		}
	}
	if err := saveSyncState(syncStatePath(cfg), state); err != nil {
		return err
	}
	fmt.Printf("Incremental sync complete: %d updated, %d unavailable.\n", updated, missing)
	return nil
}

func initializeConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("configuration already exists: %s (edit it instead of overwriting)", path)
	}
	cfg := defaultConfig()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.SessionsDir, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.ReportsDir, 0755); err != nil {
		return err
	}
	fmt.Println("Configuration created:", path)
	fmt.Println("Edit the paths if needed; use hermes-bench sync to export stored sessions into:")
	fmt.Println(" ", cfg.SessionsDir)
	fmt.Println("Telemetry log:")
	fmt.Println(" ", cfg.TelemetryFile)
	fmt.Println("Reports will be saved to:")
	fmt.Println(" ", cfg.ReportsDir)
	return nil
}

func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("configuration not found: %s\nRun 'hermes-bench init' to create it, or use --config <path>", path)
		}
		return Config{}, err
	}
	cfg := defaultConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	cfg.SessionsDir = resolveConfigPathValue(cfg.SessionsDir, path)
	cfg.TelemetryFile = resolveConfigPathValue(cfg.TelemetryFile, path)
	// Migrate the original v0.2 default location to executable-root reports.
	// Explicit custom report directories remain unchanged.
	oldBase := filepath.Join(filepath.Dir(cfg.SessionsDir), "reports")
	if filepath.Clean(expandPath(cfg.ReportsDir)) == filepath.Clean(oldBase) {
		cfg.ReportsDir = defaultReportsDir()
	} else {
		cfg.ReportsDir = resolveConfigPathValue(cfg.ReportsDir, path)
	}
	if cfg.SessionPattern == "" {
		cfg.SessionPattern = "*.jsonl"
	}
	return cfg, nil
}

func resolveConfigPathValue(value, configPath string) string {
	p := expandPath(value)
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(configPath), p)
	}
	abs, err := filepath.Abs(p)
	if err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}

var winEnvPattern = regexp.MustCompile(`%([^%]+)%`)

func expandPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = os.ExpandEnv(value)
	value = winEnvPattern.ReplaceAllStringFunc(value, func(token string) string {
		name := strings.Trim(token, "%")
		if v := os.Getenv(name); v != "" {
			return v
		}
		return token
	})
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		home, _ := os.UserHomeDir()
		value = filepath.Join(home, strings.TrimLeft(strings.TrimPrefix(value, "~"), `/\`))
	}
	return filepath.Clean(value)
}

func listSessions(cfg Config) error {
	candidates, warnings, err := discoverSessions(cfg)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		fmt.Println("HERMES BENCH — no session exports found")
		fmt.Println("Session folder:", cfg.SessionsDir)
		fmt.Println("Export a session with:")
		fmt.Println("Run: hermes-bench sync")
		return nil
	}
	fmt.Println("HERMES BENCH — AVAILABLE SESSIONS")
	fmt.Println()
	fmt.Printf("%-4s %-17s %-42s %-22s %s\n", "#", "DATE", "TITLE", "MODEL", "TELEMETRY")
	fmt.Println(strings.Repeat("─", 112))
	for i, c := range candidates {
		date := "unknown"
		if c.Started != nil {
			date = c.Started.Local().Format("2006-01-02 15:04")
		} else if !c.FileModTime.IsZero() {
			date = c.FileModTime.Local().Format("2006-01-02 15:04")
		}
		title := truncate(c.Title, 40)
		model := truncate(c.Model, 20)
		telemetry := "none"
		if c.TelemetryEvents > 0 {
			telemetry = fmt.Sprintf("%d events / %d tools", c.TelemetryEvents, c.TelemetryTools)
		}
		fmt.Printf("%-4d %-17s %-42s %-22s %s\n", i+1, date, title, model, telemetry)
	}
	fmt.Printf("\n%d session(s) found | telemetry file: %s\n", len(candidates), fileState(cfg.TelemetryFile))
	fmt.Println("Generate a report: hermes-bench report <index> (Markdown saved automatically)")
	for _, w := range warnings {
		fmt.Println("NOTE:", w)
	}
	return nil
}

func discoverSessions(cfg Config) ([]SessionCandidate, []string, error) {
	info, err := os.Stat(cfg.SessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("session export directory does not exist: %s (run 'hermes-bench init' or update config.json)", cfg.SessionsDir)
		}
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("sessions_dir is not a directory: %s", cfg.SessionsDir)
	}
	files := []string{}
	if cfg.Recursive {
		err = filepath.WalkDir(cfg.SessionsDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			matched, _ := filepath.Match(cfg.SessionPattern, d.Name())
			if matched {
				files = append(files, path)
			}
			return nil
		})
	} else {
		entries, readErr := os.ReadDir(cfg.SessionsDir)
		if readErr != nil {
			return nil, nil, readErr
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			matched, _ := filepath.Match(cfg.SessionPattern, entry.Name())
			if matched {
				files = append(files, filepath.Join(cfg.SessionsDir, entry.Name()))
			}
		}
	}
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(files)

	telemetryIndex, telemetryWarnings := buildTelemetryIndex(cfg.TelemetryFile)
	var candidates []SessionCandidate
	var warnings []string
	warnings = append(warnings, telemetryWarnings...)
	seen := map[string]int{}
	for _, path := range files {
		objects, bad, readErr := readJSONLObjects(path)
		if readErr != nil {
			warnings = append(warnings, fmt.Sprintf("Could not read %s: %v", filepath.Base(path), readErr))
			continue
		}
		if bad > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: %d malformed line(s) ignored", filepath.Base(path), bad))
		}
		fi, _ := os.Stat(path)
		for _, obj := range objects {
			session := unwrapSession(obj)
			candidate := candidateFromSession(session, path)
			if candidate.ID == "" {
				warnings = append(warnings, fmt.Sprintf("%s contains a record without a session ID; skipped", filepath.Base(path)))
				continue
			}
			if fi != nil {
				candidate.FileModTime = fi.ModTime()
			}
			if events, ok := telemetryIndex[candidate.ID]; ok {
				candidate.TelemetryEvents = events.TotalEvents
				candidate.TelemetryTools = events.ToolCompletions
			}
			// If the same session was exported more than once, retain the newest file copy.
			if oldIndex, exists := seen[candidate.ID]; exists {
				warnings = append(warnings, fmt.Sprintf("Duplicate export for session %s found; newest file copy shown", shortID(candidate.ID)))
				if candidates[oldIndex].FileModTime.After(candidate.FileModTime) {
					continue
				}
				candidates[oldIndex] = candidate
				continue
			}
			seen[candidate.ID] = len(candidates)
			candidates = append(candidates, candidate)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		ti, tj := candidateSortTime(candidates[i]), candidateSortTime(candidates[j])
		return ti.After(tj)
	})
	return candidates, uniqueStrings(warnings), nil
}

func candidateSortTime(c SessionCandidate) time.Time {
	if c.Started != nil {
		return *c.Started
	}
	return c.FileModTime
}

func candidateFromSession(m map[string]any, path string) SessionCandidate {
	c := SessionCandidate{
		Path: path, ID: getString(m, "id", "session_id", "sessionId"),
		Title: getString(m, "title", "name"), Model: getString(m, "model", "model_name"),
		Provider: getString(m, "provider", "provider_name"), Source: getString(m, "source", "platform"),
		CWD: getString(m, "cwd", "working_directory", "workdir"), Raw: m,
	}
	if c.Title == "" {
		c.Title = "Untitled Hermes session"
	}
	if t, ok := parseTime(first(m, "started_at", "created_at", "start_time")); ok {
		c.Started = &t
	}
	if t, ok := parseTime(first(m, "ended_at", "completed_at", "end_time", "last_active_at")); ok {
		c.Ended = &t
	}
	c.InputTokens = findInt(m, []string{"input_tokens", "prompt_tokens", "total_input_tokens", "tokens_input"})
	c.OutputTokens = findInt(m, []string{"output_tokens", "completion_tokens", "total_output_tokens", "tokens_output"})
	messages, _ := asSlice(m["messages"])
	c.Messages = len(messages)
	for _, item := range messages {
		msg, ok := asMap(item)
		if !ok {
			continue
		}
		active, hasActive := msg["active"].(bool)
		if !hasActive || active {
			c.ActiveMessages++
		} else {
			c.ArchivedMessages++
		}
		calls := decodeToolCalls(msg["tool_calls"])
		for range calls {
			c.ToolCalls++
			if !hasActive || active {
				c.ActiveToolCalls++
			} else {
				c.ArchivedToolCalls++
			}
		}
	}
	return c
}

type TelemetryIndexEntry struct{ TotalEvents, ToolCompletions int }

func buildTelemetryIndex(path string) (map[string]TelemetryIndexEntry, []string) {
	index := map[string]TelemetryIndexEntry{}
	if path == "" {
		return index, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return index, []string{"Telemetry log not found; session listing will be transcript-only."}
		}
		return index, []string{fmt.Sprintf("Cannot access telemetry log: %v", err)}
	}
	objects, bad, err := readJSONLObjects(path)
	if err != nil {
		return index, []string{fmt.Sprintf("Cannot read telemetry log: %v", err)}
	}
	warnings := []string{}
	if bad > 0 {
		warnings = append(warnings, fmt.Sprintf("Telemetry log has %d malformed line(s).", bad))
	}
	unattributedToolEvents := 0
	for _, obj := range objects {
		name := getString(obj, "event", "type")
		if !strings.HasPrefix(name, "tool.") && name != "pre_tool_call" && name != "post_tool_call" {
			continue
		}
		key := telemetrySessionKey(obj)
		if key == "" {
			unattributedToolEvents++
			continue
		}
		entry := index[key]
		entry.TotalEvents++
		if name == "tool.completed" || name == "post_tool_call" {
			entry.ToolCompletions++
		}
		index[key] = entry
	}
	if unattributedToolEvents > 0 {
		warnings = append(warnings, fmt.Sprintf("%d tool telemetry event(s) without a session/task ID will be excluded from session reports.", unattributedToolEvents))
	}
	return index, warnings
}

func telemetrySessionKey(m map[string]any) string {
	if s := getString(m, "session_id", "sessionId"); s != "" {
		return s
	}
	if s := getString(m, "task_id", "taskId"); s != "" {
		return s
	}
	if s := getString(m, "conversation_id", "conversationId"); s != "" {
		return s
	}
	return ""
}

func reportSession(cfg Config, index int, markdown bool) error {
	candidates, _, err := discoverSessions(cfg)
	if err != nil {
		return err
	}
	if index < 1 || index > len(candidates) {
		return fmt.Errorf("index %d is out of range; run 'hermes-bench list' to see available sessions (1-%d)", index, len(candidates))
	}
	candidate := candidates[index-1]
	a, err := analyze(candidate, cfg.TelemetryFile)
	if err != nil {
		return err
	}
	// Save the detailed report first: the terminal summary only advertises a path
	// after the Markdown file was successfully written.
	reportPath := ""
	if markdown {
		if err := os.MkdirAll(cfg.ReportsDir, 0755); err != nil {
			return fmt.Errorf("create reports directory: %w", err)
		}
		// Keep each run under history before updating the human-friendly latest path.
		// This prevents same-title benchmark runs from silently replacing one another.
		historyDir := filepath.Join(cfg.ReportsDir, "history")
		if err := os.MkdirAll(historyDir, 0755); err != nil {
			return fmt.Errorf("create report history directory: %w", err)
		}
		archivedPath := filepath.Join(historyDir, historyReportFilename(candidate))
		content := []byte(renderMarkdown(a))
		if err := os.WriteFile(archivedPath, content, 0644); err != nil {
			return fmt.Errorf("write historical Markdown report: %w", err)
		}
		reportPath = archivedPath
		// Only the newest exported session updates the short, title-only filename.
		if index == 1 {
			reportPath = filepath.Join(cfg.ReportsDir, reportFilename(candidate))
			if err := os.WriteFile(reportPath, content, 0644); err != nil {
				return fmt.Errorf("write latest Markdown report: %w", err)
			}
		}
	}
	fmt.Print(renderConsoleSummary(a, reportPath))
	return nil
}

func analyze(c SessionCandidate, telemetryPath string) (*Analysis, error) {
	m := c.Raw
	a := &Analysis{
		SessionID: c.ID, Title: c.Title, Model: c.Model, Provider: c.Provider, Source: c.Source, CWD: c.CWD,
		SessionPath: c.Path, Started: c.Started, Ended: c.Ended,
		Messages: c.Messages, ActiveMessages: c.ActiveMessages, ArchivedMessages: c.ArchivedMessages,
		ToolCalls: c.ToolCalls, ActiveToolCalls: c.ActiveToolCalls, ArchivedToolCalls: c.ArchivedToolCalls,
		InputTokens:      findInt(m, []string{"input_tokens", "prompt_tokens", "total_input_tokens", "tokens_input"}),
		OutputTokens:     findInt(m, []string{"output_tokens", "completion_tokens", "total_output_tokens", "tokens_output"}),
		CacheReadTokens:  findInt(m, []string{"cache_read_tokens", "cached_input_tokens", "input_cache_read_tokens", "cache_tokens"}),
		CacheWriteTokens: findInt(m, []string{"cache_write_tokens", "cache_creation_tokens", "input_cache_write_tokens"}),
		ReasoningTokens:  findInt(m, []string{"reasoning_tokens", "total_reasoning_tokens"}),
		CostUSD:          findFloat(m, []string{"cost_usd", "estimated_cost_usd", "total_cost_usd"}),
		ToolNames:        map[string]int{}, ToolStats: map[string]*ToolStat{},
	}
	if a.Started != nil && a.Ended != nil && !a.Ended.Before(*a.Started) {
		d := float64(a.Ended.Sub(*a.Started).Milliseconds())
		a.WallMS = &d
	}
	if timings, ok := asMap(m["timings"]); ok {
		if a.WallMS == nil {
			if n, ok := number(first(timings, "wall_clock_ms")); ok {
				a.WallMS = &n
			}
		}
		if n, ok := number(first(timings, "largest_gap_ms")); ok {
			a.LargestGapMS = &n
		}
		if b, ok := timings["complete"].(bool); ok {
			a.TimingComplete = &b
		}
		if xs, ok := asSlice(timings["intervals"]); ok {
			for _, item := range xs {
				iv, ok := asMap(item)
				if !ok {
					continue
				}
				gap, ok := number(iv["gap_ms"])
				if !ok {
					continue
				}
				a.TopGaps = append(a.TopGaps, Gap{FromRole: getString(iv, "from_role"), ToRole: getString(iv, "to_role"), GapMS: gap})
			}
			sort.Slice(a.TopGaps, func(i, j int) bool { return a.TopGaps[i].GapMS > a.TopGaps[j].GapMS })
			if len(a.TopGaps) > 3 {
				a.TopGaps = a.TopGaps[:3]
			}
		}
	}
	messages, _ := asSlice(m["messages"])
	a.Transcript = extractTranscript(messages)
	for _, item := range messages {
		msg, ok := asMap(item)
		if !ok {
			continue
		}
		active, hasActive := msg["active"].(bool)
		calls := decodeToolCalls(msg["tool_calls"])
		for _, call := range calls {
			name := toolCallName(call)
			if name == "" {
				name = "unknown"
			}
			if !hasActive || active {
				a.ToolNames[name]++
			}
		}
	}
	for _, item := range messages {
		if msg, ok := asMap(item); ok && strings.EqualFold(getString(msg, "role"), "user") {
			if active, exists := msg["active"].(bool); !exists || active {
				a.SessionUserMessages++
			}
		}
	}
	if a.SessionUserMessages > 1 {
		a.Warnings = append(a.Warnings, fmt.Sprintf("Multi-turn session (%d user messages): totals are session-wide, not a single experimental run.", a.SessionUserMessages))
	}
	if a.ToolCalls == 0 {
		// Legacy fallback: count tool-result rows only when no emitted tool_calls are present.
		for _, item := range messages {
			msg, ok := asMap(item)
			if !ok {
				continue
			}
			role := strings.ToLower(getString(msg, "role"))
			if role == "tool" || getString(msg, "tool_name") != "" {
				name := getString(msg, "tool_name", "name")
				if name == "" {
					name = "unknown"
				}
				a.ToolCalls++
				active, ok := msg["active"].(bool)
				if !ok || active {
					a.ActiveToolCalls++
					a.ToolNames[name]++
				} else {
					a.ArchivedToolCalls++
				}
			}
		}
	}
	a.SessionSHA256, _ = fileSHA256(c.Path)
	if telemetryPath != "" {
		if err := addTelemetry(a, telemetryPath); err != nil {
			return nil, err
		}
	}
	if a.Model == "" {
		a.Warnings = append(a.Warnings, "Model metadata is unavailable.")
	}
	if a.InputTokens == nil || a.OutputTokens == nil {
		a.Warnings = append(a.Warnings, "Input/output token totals were not found in the recognized session metadata fields.")
	}
	if a.WallMS == nil {
		a.Warnings = append(a.Warnings, "Session start/end timestamps are unavailable.")
	}
	if a.TimingComplete != nil && !*a.TimingComplete {
		a.Warnings = append(a.Warnings, "Hermes timing data is derived from message timestamps and marked incomplete; it is not an active-work stopwatch.")
	}
	return a, nil
}

func addTelemetry(a *Analysis, path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			a.Warnings = append(a.Warnings, "Telemetry log was not found; report uses session export only.")
			return nil
		}
		return err
	}
	objects, bad, err := readJSONLObjects(path)
	if err != nil {
		return err
	}
	a.TelemetryBadLines = bad
	a.TelemetrySHA256, _ = fileSHA256(path)
	var intervals []Interval
	toolStarts := 0
	// Turn events are paired in order per session because the current plugin's turn hooks
	// do not consistently expose a dedicated turn ID.
	var turnStarts []time.Time
	for _, obj := range objects {
		name := getString(obj, "event", "type")
		if !strings.HasPrefix(name, "tool.") && !strings.HasPrefix(name, "api.") && !strings.HasPrefix(name, "stream.") && name != "pre_tool_call" && name != "post_tool_call" && name != "turn.started" && name != "turn.response" && name != "turn.ended" && name != "session.started" {
			continue
		}
		key := telemetrySessionKey(obj)
		if key == "" {
			if strings.HasPrefix(name, "tool.") || name == "pre_tool_call" || name == "post_tool_call" {
				a.TelemetryUnattributed++
			}
			continue
		}
		if key != a.SessionID {
			continue
		}
		e := Event{Raw: obj, Name: name, Tool: getString(obj, "tool", "tool_name"), SessionID: getString(obj, "session_id", "sessionId"), TaskID: getString(obj, "task_id", "taskId"), CallID: getString(obj, "tool_call_id", "call_id"), Status: strings.ToLower(getString(obj, "status", "outcome")), ErrorType: getString(obj, "error_type")}
		if t, ok := parseTime(first(obj, "ts", "timestamp", "time")); ok {
			e.TS = t
			if a.TelemetryFirst == nil || t.Before(*a.TelemetryFirst) {
				tt := t
				a.TelemetryFirst = &tt
			}
			if a.TelemetryLast == nil || t.After(*a.TelemetryLast) {
				tt := t
				a.TelemetryLast = &tt
			}
		}
		if n, ok := number(first(obj, "duration_ms", "elapsed_ms")); ok {
			e.Duration = n
		}
		if n, ok := number(first(obj, "result_bytes", "output_bytes", "bytes")); ok {
			e.Bytes = int64(n)
		}
		if n, ok := number(first(obj, "result_chars", "output_chars", "chars")); ok {
			e.Chars = int64(n)
		}
		a.TelemetryEvents++
		switch name {
		case "turn.started":
			if !e.TS.IsZero() {
				turnStarts = append(turnStarts, e.TS)
				if a.FirstTurnStart == nil || e.TS.Before(*a.FirstTurnStart) {
					t := e.TS
					a.FirstTurnStart = &t
				}
			}
		case "turn.response":
			if !e.TS.IsZero() && len(turnStarts) > 0 {
				st := turnStarts[0]
				turnStarts = turnStarts[1:]
				if !e.TS.Before(st) {
					a.TurnWindows = append(a.TurnWindows, Interval{Start: st, End: e.TS})
					d := float64(e.TS.Sub(st).Milliseconds())
					a.TurnCount++
					a.TurnDurationTotalMS += d
					if d > a.TurnDurationMaxMS {
						a.TurnDurationMaxMS = d
					}
				}
			}
		case "api.started":
			a.API.Started++
			if !e.TS.IsZero() && (a.FirstAPIStart == nil || e.TS.Before(*a.FirstAPIStart)) {
				t := e.TS
				a.FirstAPIStart = &t
			}
			if a.API.Providers == nil {
				a.API.Providers = map[string]int{}
			}
			if a.API.Models == nil {
				a.API.Models = map[string]int{}
			}
			if provider := getString(obj, "provider"); provider != "" {
				a.API.Providers[provider]++
			}
			if model := getString(obj, "model"); model != "" {
				a.API.Models[model]++
			}
		case "api.completed":
			a.API.Completed++
			if d, ok := number(obj["api_duration"]); ok {
				a.API.TotalSeconds += d
			}
			row := APIRow{
				RequestID:        getString(obj, "api_request_id"),
				Model:            getString(obj, "model", "response_model"),
				Provider:         getString(obj, "provider"),
				FinishReason:     getString(obj, "finish_reason"),
				PromptTokens:     findIntDirect(obj, "usage_prompt_tokens", "usage_input_tokens"),
				CompletionTokens: findIntDirect(obj, "usage_completion_tokens", "usage_output_tokens"),
				CacheReadTokens:  findIntDirect(obj, "usage_cache_read_tokens"),
				CacheWriteTokens: findIntDirect(obj, "usage_cache_write_tokens"),
				ReasoningTokens:  findIntDirect(obj, "usage_reasoning_tokens"),
				TotalTokens:      findIntDirect(obj, "usage_total_tokens"),
			}
			if d, ok := number(obj["api_duration"]); ok {
				row.DurationSeconds = &d
			}
			a.APIUsageRows = append(a.APIUsageRows, row)
		case "api.error":
			a.API.Errors++
		case "stream.started":
			a.Stream.Started++
		case "stream.ended":
			a.Stream.Ended++
			row := StreamRow{}
			row.DurationMS, _ = number(obj["observed_duration_ms"])
			row.MaxGapMS, _ = number(obj["max_delta_gap_ms"])
			if v, ok := number(obj["text_deltas"]); ok {
				row.TextDeltas = int(v)
			}
			if v, ok := number(obj["reasoning_deltas"]); ok {
				row.ReasoningDeltas = int(v)
			}
			if v, ok := number(obj["first_text_ms"]); ok {
				row.FirstText = &v
				a.Stream.FirstTextMS = append(a.Stream.FirstTextMS, v)
			}
			if v, ok := number(obj["last_text_ms"]); ok {
				row.LastText = &v
			}
			if v, ok := number(obj["first_reasoning_ms"]); ok {
				row.FirstReasoning = &v
				a.Stream.FirstReasoningMS = append(a.Stream.FirstReasoningMS, v)
			}
			if v, ok := number(obj["last_reasoning_ms"]); ok {
				row.LastReasoning = &v
			}
			if row.ReasoningDeltas > 0 {
				a.Stream.WithReasoning++
			}
			if row.TextDeltas > 0 {
				a.Stream.WithText++
			}
			a.Stream.TextDeltas += row.TextDeltas
			a.Stream.ReasoningDeltas += row.ReasoningDeltas
			a.Stream.TotalObservedMS += row.DurationMS
			if row.MaxGapMS > a.Stream.MaxGapMS {
				a.Stream.MaxGapMS = row.MaxGapMS
			}
			if row.FirstReasoning != nil && row.LastReasoning != nil {
				a.Stream.ObservedReasoningSpans = append(a.Stream.ObservedReasoningSpans, *row.LastReasoning-*row.FirstReasoning)
			}
			if row.FirstText != nil && row.LastText != nil && *row.LastText >= *row.FirstText && row.DurationMS >= *row.LastText {
				a.Stream.ObservedTextSpans = append(a.Stream.ObservedTextSpans, *row.LastText-*row.FirstText)
				if !e.TS.IsZero() {
					base := e.TS.Add(-time.Duration(row.DurationMS * float64(time.Millisecond)))
					a.TextWindows = append(a.TextWindows, Interval{
						Start: base.Add(time.Duration(*row.FirstText * float64(time.Millisecond))),
						End:   base.Add(time.Duration(*row.LastText * float64(time.Millisecond))),
					})
				}
			}
			a.Stream.Records = append(a.Stream.Records, row)
		case "tool.started", "pre_tool_call":
			toolStarts++
		case "tool.completed", "post_tool_call":
			if e.Tool == "" {
				e.Tool = "unknown"
			}
			st := a.ToolStats[e.Tool]
			if st == nil {
				st = &ToolStat{}
				a.ToolStats[e.Tool] = st
			}
			st.Calls++
			st.TotalMS += e.Duration
			if e.Duration > st.MaxMS {
				st.MaxMS = e.Duration
			}
			st.ResultBytes += e.Bytes
			st.ResultChars += e.Chars
			a.TelemetryToolMS += e.Duration
			a.TelemetryResultBytes += e.Bytes
			a.TelemetryResultChars += e.Chars
			switch e.Status {
			case "ok", "success", "succeeded", "completed":
			case "error", "failed", "failure":
				st.Failures++
				a.TelemetryFailures++
			default:
				if e.ErrorType != "" {
					st.Failures++
					a.TelemetryFailures++
				} else {
					st.Unknown++
					a.TelemetryUnknownOutcomes++
				}
			}
			if !e.TS.IsZero() && e.Duration > 0 {
				end := e.TS
				start := end.Add(-time.Duration(e.Duration * float64(time.Millisecond)))
				iv := Interval{Start: start, End: end}
				intervals = append(intervals, iv)
				a.ToolWindows = append(a.ToolWindows, iv)
			}
		}
	}
	a.Stream.MissingEnd = max(0, a.Stream.Started-a.Stream.Ended)
	if a.API.Started != a.API.Completed+a.API.Errors && a.API.Started > 0 {
		a.Warnings = append(a.Warnings, fmt.Sprintf("API hook count differs: %d starts, %d completions, %d errors; retry/error hooks may overlap.", a.API.Started, a.API.Completed, a.API.Errors))
	}
	if a.Stream.MissingEnd > 0 {
		a.Warnings = append(a.Warnings, fmt.Sprintf("%d streams have no observed end event.", a.Stream.MissingEnd))
	}
	a.TelemetryStarted = toolStarts
	for _, s := range a.ToolStats {
		a.TelemetryCompleted += s.Calls
	}
	a.TelemetryToolUnionMS = intervalUnion(intervals)
	if bad > 0 {
		a.Warnings = append(a.Warnings, fmt.Sprintf("Telemetry log contains %d malformed line(s); valid records were analyzed.", bad))
	}
	if a.TelemetryUnattributed > 0 {
		a.Warnings = append(a.Warnings, fmt.Sprintf("%d tool event(s) without a session/task ID were excluded.", a.TelemetryUnattributed))
	}
	if a.TelemetryEvents == 0 {
		a.Warnings = append(a.Warnings, "No telemetry events matched this session ID.")
	}
	if toolStarts != a.TelemetryCompleted {
		a.Warnings = append(a.Warnings, fmt.Sprintf("Tool hook count differs: %d starts and %d completions.", toolStarts, a.TelemetryCompleted))
	}
	if a.ToolCalls > 0 && a.TelemetryCompleted > 0 && a.ActiveToolCalls != a.TelemetryCompleted {
		a.Warnings = append(a.Warnings, fmt.Sprintf("Active transcript tool calls (%d) differ from telemetry completions (%d). Check subagents, archived messages and missing hooks.", a.ActiveToolCalls, a.TelemetryCompleted))
	}
	if a.TelemetryUnknownOutcomes > 0 {
		a.Warnings = append(a.Warnings, fmt.Sprintf("Outcome is unknown for %d completed tool call(s); they are not counted as successes.", a.TelemetryUnknownOutcomes))
	}
	if len(turnStarts) > 0 {
		a.Warnings = append(a.Warnings, fmt.Sprintf("%d turn.started event(s) had no matching turn.response.", len(turnStarts)))
	}
	return nil
}

func intervalUnion(intervals []Interval) float64 {
	if len(intervals) == 0 {
		return 0
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].Start.Before(intervals[j].Start) })
	start, end := intervals[0].Start, intervals[0].End
	var total time.Duration
	for _, iv := range intervals[1:] {
		if !iv.Start.After(end) {
			if iv.End.After(end) {
				end = iv.End
			}
			continue
		}
		total += end.Sub(start)
		start, end = iv.Start, iv.End
	}
	total += end.Sub(start)
	return float64(total.Milliseconds())
}

// renderConsoleSummary keeps the user-facing console in the requested compact
// format. Tool token estimates use result character counts, NOT provider usage.
// Prompt/background/final estimates are a deliberately rough workload partition.
func renderConsoleSummary(a *Analysis, reportPath string) string {
	var b strings.Builder
	model := compactModel(a.Model)
	turnSeconds := a.TurnDurationTotalMS / 1000
	if a.TurnCount == 1 {
		fmt.Fprintf(&b, "%s completed the task in %.1fs", model, turnSeconds)
	} else if a.TurnCount > 1 {
		fmt.Fprintf(&b, "%s completed %d turns in %.1fs combined", model, a.TurnCount, turnSeconds)
	} else if a.WallMS != nil {
		fmt.Fprintf(&b, "%s recorded %.1fs of session activity", model, *a.WallMS/1000)
	} else {
		fmt.Fprintf(&b, "%s ran (duration unavailable)", model)
	}

	fmt.Fprint(&b, " (")
	if ms, source, ok := kickoffDelay(a); ok {
		fmt.Fprintf(&b, "%s before %s", formatKickoff(ms), source)
	} else {
		fmt.Fprint(&b, "kickoff unavailable")
	}
	fmt.Fprintf(&b, "; %d tool calls across %d API calls; ", a.TelemetryCompleted, a.API.Started)
	if a.CostUSD != nil {
		fmt.Fprintf(&b, "$%.4f", *a.CostUSD)
	} else {
		fmt.Fprint(&b, "cost unavailable")
	}
	fmt.Fprint(&b, ")")
	if a.TelemetryToolMS > 0 && a.TelemetryToolUnionMS > 0 {
		fmt.Fprintf(&b, ", spending %.1fs waiting on tools (%.1fs total across individual tool calls):\n", a.TelemetryToolUnionMS/1000, a.TelemetryToolMS/1000)
	} else {
		fmt.Fprintln(&b, ":")
	}
	if len(a.ToolStats) > 0 {
		names := sortedToolNames(a.ToolStats)
		width := 0
		for _, name := range names {
			if len(name) > width {
				width = len(name)
			}
		}
		for _, name := range names {
			st := a.ToolStats[name]
			tokenLabel := humanBytes(st.ResultBytes) + " results"
			if st.ResultChars > 0 {
				tokenLabel = "~" + compactTokens(roughTokenCountFromChars(st.ResultChars)) + " toks"
			}
			fmt.Fprintf(&b, "  %-*s  %7s | %s\n", width, name, formatToolDuration(st.TotalMS), tokenLabel)
		}
	}

	prompt, final, background, _, count, ok := approximateWorkload(a)
	if ok {
		fmt.Fprintf(&b, "using ~%s prompt toks (x%d = ~%s cumulative), ~%s background workload toks, and ~%s final-answer toks.\n",
			compactTokens(prompt), count, compactTokens(prompt*int64(count)), compactTokens(background), compactTokens(final))
		// The residual is a partition of provider-reported cumulative input and output.
		// It includes repeat context and cache reads, not just generated reasoning.
	} else {
		fmt.Fprintln(&b, "Token workload estimate: unavailable (missing transcript or reconciled provider usage).")
	}
	apiParts := []string{}
	if totalInput, _, verified := verifiedFreshTokenUsage(a); verified {
		apiParts = append(apiParts, fmt.Sprintf("%s total input (%s uncached, %s cached)", compactTokens(totalInput), compactTokens(*a.InputTokens), compactTokens(*a.CacheReadTokens)))
	} else if a.InputTokens != nil {
		apiParts = append(apiParts, compactTokens(*a.InputTokens)+" reported input")
	}
	if a.OutputTokens != nil {
		output := compactTokens(*a.OutputTokens) + " output"
		if a.ReasoningTokens != nil {
			output += " (" + compactTokens(*a.ReasoningTokens) + " reported reasoning; not added again)"
		}
		apiParts = append(apiParts, output)
	}
	if len(apiParts) > 0 {
		fmt.Fprintf(&b, "  API: %s.\n", strings.Join(apiParts, ", "))
	}

	if a.SessionUserMessages > 1 {
		fmt.Fprintf(&b, "Multi-turn session: %d user messages; totals are session-wide.\n", a.SessionUserMessages)
	}
	toolsOK := a.TelemetryStarted == a.TelemetryCompleted && a.TelemetryCompleted == a.ActiveToolCalls && a.TelemetryBadLines == 0 && a.TelemetryUnattributed == 0
	apiOK := a.API.Started == a.API.Completed && a.API.Errors == 0
	streamOK := a.Stream.Started == a.Stream.Ended && a.Stream.MissingEnd == 0
	if toolsOK && apiOK && streamOK && a.API.Started > 0 {
		fmt.Fprintln(&b, "Telemetry matches.")
	} else {
		fmt.Fprintf(&b, "Telemetry incomplete: tools %d/%d, API %d/%d (%d errors), streams %d/%d.\n", a.TelemetryCompleted, a.ActiveToolCalls, a.API.Completed, a.API.Started, a.API.Errors, a.Stream.Ended, a.Stream.Started)
	}
	if reportPath != "" {
		fmt.Fprintf(&b, "Full report: %s\n", reportPath)
	}
	return b.String()
}

// Rough text-only estimate: ~4 ASCII characters per token, and ~1 token for
// each CJK ideograph/syllabic character. This is not the provider tokenizer.
func roughTokenCount(text string) int64 {
	var ascii, cjk int64
	for _, r := range text {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			cjk++
		} else {
			ascii++
		}
	}
	return cjk + (ascii+3)/4
}

func roughTokenCountFromChars(chars int64) int64 {
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}

func formatToolDuration(ms float64) string {
	if ms < 1000 {
		return fmt.Sprintf("%.0fms", ms)
	}
	return fmt.Sprintf("%.2fs", ms/1000)
}

// approximateWorkload partitions total cumulative provider input+output into
// prompt occurrences, all remaining work, and visible final-answer text.
// Every request is ASSUMED to resend the same user prompt; this assumption is
// not independently validated against the actual provider request payloads.
func approximateWorkload(a *Analysis) (prompt, final, background, total int64, requests int, ok bool) {
	input, _, verified := verifiedFreshTokenUsage(a)
	if !verified || a.SessionUserMessages != 1 || a.API.Started != a.API.Completed || a.API.Started < 1 {
		return
	}
	var promptText, answerText string
	for _, msg := range a.Transcript {
		if !msg.Active {
			continue
		}
		if strings.EqualFold(msg.Role, "user") && strings.TrimSpace(msg.Content) != "" {
			promptText = msg.Content
		}
		if strings.EqualFold(msg.Role, "assistant") && len(msg.ToolCalls) == 0 && strings.TrimSpace(msg.Content) != "" {
			answerText = msg.Content
		}
	}
	if promptText == "" || answerText == "" {
		return
	}
	prompt = roughTokenCount(promptText)
	final = roughTokenCount(answerText)
	requests = a.API.Started
	total = input + *a.OutputTokens
	background = total - prompt*int64(requests) - final
	if prompt <= 0 || final <= 0 || background < 0 {
		return 0, 0, 0, 0, 0, false
	}
	return prompt, final, background, total, requests, true
}

// kickoffDelay measures from the first persisted user message timestamp to the
// first observed provider request. The persisted timestamp is not a guaranteed
// wall-clock moment when the user pressed Enter.
func kickoffDelay(a *Analysis) (float64, string, bool) {
	if a.SessionUserMessages != 1 {
		return 0, "", false
	}
	var userTS *time.Time
	for _, m := range a.Transcript {
		if m.Active && strings.EqualFold(m.Role, "user") && m.Timestamp != nil {
			if userTS == nil || m.Timestamp.Before(*userTS) {
				t := *m.Timestamp
				userTS = &t
			}
		}
	}
	if userTS == nil {
		return 0, "", false
	}
	providerTS := a.FirstAPIStart
	label := "first model request"
	if providerTS == nil {
		providerTS = a.FirstTurnStart
		label = "agent turn start"
	}
	if providerTS == nil {
		return 0, "", false
	}
	ms := float64(providerTS.Sub(*userTS)) / float64(time.Millisecond)
	if ms < 0 || ms > 60_000 {
		return 0, "", false
	}
	return ms, label, true
}

func formatKickoff(ms float64) string {
	if ms < 1000 {
		return fmt.Sprintf("%.0fms", ms)
	}
	return fmt.Sprintf("%.2fs", ms/1000)
}

// observedTimePartition makes mutually exclusive sections of ONE measured
// turn. Tool activity takes priority when tool and text intervals overlap.
// The residual includes provider/network waits, agent work, and unobserved time.
func observedTimePartition(a *Analysis) (other, toolMS, textMS float64, ok bool) {
	if a.SessionUserMessages != 1 || len(a.TurnWindows) != 1 || len(a.TextWindows) == 0 || len(a.ToolWindows) == 0 {
		return 0, 0, 0, false
	}
	turn := a.TurnWindows[0]
	if !turn.End.After(turn.Start) {
		return 0, 0, 0, false
	}
	tools := clippedIntervals(a.ToolWindows, turn)
	texts := clippedIntervals(a.TextWindows, turn)
	if len(tools) == 0 || len(texts) == 0 {
		return 0, 0, 0, false
	}
	toolMS = intervalUnion(tools)
	all := append(append([]Interval{}, tools...), texts...)
	activeMS := intervalUnion(all)
	textMS = activeMS - toolMS
	other = float64(turn.End.Sub(turn.Start))/float64(time.Millisecond) - activeMS
	if other < 0 && other > -2 { // roundoff at millisecond boundaries
		other = 0
	}
	return other, toolMS, textMS, other >= 0 && textMS >= 0
}

func clippedIntervals(intervals []Interval, boundary Interval) []Interval {
	var out []Interval
	for _, iv := range intervals {
		start, end := iv.Start, iv.End
		if start.Before(boundary.Start) {
			start = boundary.Start
		}
		if end.After(boundary.End) {
			end = boundary.End
		}
		if end.After(start) {
			out = append(out, Interval{Start: start, End: end})
		}
	}
	return out
}

// Only expose a derived noncached I/O total when per-request provider usage
// reconciles with the session's aggregate counters. Reasoning is NOT added.
func verifiedFreshTokenUsage(a *Analysis) (totalInput, noncachedIO int64, ok bool) {
	if len(a.APIUsageRows) == 0 || len(a.APIUsageRows) != a.API.Completed || a.InputTokens == nil || a.CacheReadTokens == nil || a.OutputTokens == nil {
		return 0, 0, false
	}
	var cached, output int64
	for _, row := range a.APIUsageRows {
		if row.PromptTokens == nil || row.CacheReadTokens == nil || row.CompletionTokens == nil {
			return 0, 0, false
		}
		if *row.PromptTokens < *row.CacheReadTokens || *row.CacheReadTokens < 0 {
			return 0, 0, false
		}
		totalInput += *row.PromptTokens
		cached += *row.CacheReadTokens
		output += *row.CompletionTokens
	}
	if cached != *a.CacheReadTokens || totalInput-cached != *a.InputTokens || output != *a.OutputTokens {
		return 0, 0, false
	}
	return totalInput, *a.InputTokens + output, true
}

// Preserve all assumptions alongside the provider counters in Markdown.
func appendWorkloadMarkdown(b *strings.Builder, a *Analysis) {
	fmt.Fprint(b, "\n## Estimated prompt / background / final workload\n\n")
	prompt, final, background, total, calls, ok := approximateWorkload(a)
	if !ok {
		fmt.Fprint(b, "Unavailable: requires one active user prompt, a saved final assistant message, and complete reconciled per-request provider usage.\n")
		return
	}
	fmt.Fprintf(b, "| Component | Estimated tokens |\n|---|---:|\n| Original user prompt (once) | ~%d |\n| Prompt repeated (assuming %d identical request inclusions) | ~%d |\n| Remaining background workload | ~%d |\n| Final visible answer | ~%d |\n| Provider cumulative input + output | %d |\n",
		prompt, calls, prompt*int64(calls), background, final, total)
	fmt.Fprint(b, "\nThese are **not provider-tokenized text counts**. Plain message content is estimated using roughly four ASCII characters per token (CJK script characters counted individually); timestamps and JSON metadata are excluded. The prompt is assumed to be present unchanged in every API request; history truncation, compaction and provider-specific framing can invalidate this assumption. Background is **the residual of cumulative provider input+output**, including cache-read input, system and tool context, repeated messages, and intermediate model output; it is **not** an estimate of internal thinking tokens. The final answer estimate may not match billed completion tokens. Redaction can alter estimates. Do not add reported reasoning tokens to output again.\n")
	if len(a.ToolStats) > 0 {
		fmt.Fprint(b, "\n### Approximate tool-result text sizes\n\n| Tool | Reported characters | Rough tokens |\n|---|---:|---:|\n")
		for _, name := range sortedToolNames(a.ToolStats) {
			st := a.ToolStats[name]
			if st.ResultChars > 0 {
				fmt.Fprintf(b, "| `%s` | %d | ~%d |\n", md(name), st.ResultChars, roughTokenCountFromChars(st.ResultChars))
			} else {
				fmt.Fprintf(b, "| `%s` | unavailable | n/a |\n", md(name))
			}
		}
		fmt.Fprint(b, "\nEstimates are based on returned character counts (~4 characters/token), not the model tokenizer. Tool-result sizes are **not additive** to provider input totals: tool results can be resent in later requests, truncated, transformed or omitted.\n")
	}
}

func compactModel(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.TrimSuffix(s, ":free")
	return valueOr(s, "unknown model")
}

func displayProvider(a *Analysis) string {
	provider := strings.TrimSpace(a.Provider)
	if provider == "" && len(a.API.Providers) == 1 {
		for name := range a.API.Providers {
			provider = name
		}
	}
	if provider == "" {
		return ""
	}
	runes := []rune(provider)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func callCountLabel(n int) string {
	if n == 1 {
		return "1 call"
	}
	return fmt.Sprintf("%d calls", n)
}

func compactTokens(n int64) string {
	if n < 1000 && n > -1000 {
		return strconv.FormatInt(n, 10)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

func sumFloat64(xs []float64) float64 {
	sum := 0.0
	for _, v := range xs {
		sum += v
	}
	return sum
}

func average(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	total := 0.0
	for _, x := range xs {
		total += x
	}
	return total / float64(len(xs))
}
func sortedCountKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func printAPIConsole(a *Analysis) {
	if a.API.Started+a.API.Completed+a.API.Errors > 0 {
		fmt.Println("\nAPI REQUESTS")
		fmt.Printf("  Started / completed / errors: %d / %d / %d\n", a.API.Started, a.API.Completed, a.API.Errors)
		if a.API.Completed > 0 {
			fmt.Printf("  API duration (sum): %.2fs; average: %.2fs\n", a.API.TotalSeconds, a.API.TotalSeconds/float64(a.API.Completed))
		}
		for _, k := range sortedCountKeys(a.API.Models) {
			fmt.Printf("  Model: %s (%d requests)\n", k, a.API.Models[k])
		}
		for _, k := range sortedCountKeys(a.API.Providers) {
			fmt.Printf("  Provider: %s (%d requests)\n", k, a.API.Providers[k])
		}
	}
	if a.Stream.Started+a.Stream.Ended > 0 {
		fmt.Println("\nSTREAMING (OBSERVED)")
		fmt.Printf("  Started / ended: %d / %d\n", a.Stream.Started, a.Stream.Ended)
		fmt.Printf("  With reasoning / text: %d / %d streams\n", a.Stream.WithReasoning, a.Stream.WithText)
		fmt.Printf("  Reasoning / text chunks: %d / %d\n", a.Stream.ReasoningDeltas, a.Stream.TextDeltas)
		if len(a.Stream.FirstReasoningMS) > 0 {
			fmt.Printf("  Average first reasoning chunk: %s\n", formatMS(average(a.Stream.FirstReasoningMS)))
		}
		if len(a.Stream.FirstTextMS) > 0 {
			fmt.Printf("  Average first text chunk: %s\n", formatMS(average(a.Stream.FirstTextMS)))
		}
		if len(a.Stream.ObservedReasoningSpans) > 0 {
			fmt.Printf("  Average reasoning-chunk span: %s\n", formatMS(average(a.Stream.ObservedReasoningSpans)))
		}
		if len(a.Stream.ObservedTextSpans) > 0 {
			fmt.Printf("  Average text-chunk span: %s\n", formatMS(average(a.Stream.ObservedTextSpans)))
		}
		fmt.Printf("  Longest observed chunk gap: %s\n", formatMS(a.Stream.MaxGapMS))
		fmt.Println("  Note: callback timestamps measure observed deltas, not private model thinking.")
	}
}
func fmtMaybeMS(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return formatMS(*v)
}
func appendAPIMarkdown(b *strings.Builder, a *Analysis) {
	if a.API.Started+a.API.Completed+a.API.Errors > 0 {
		fmt.Fprintf(b, "\n## API requests\n\n- Starts / completions / errors: **%d / %d / %d**\n", a.API.Started, a.API.Completed, a.API.Errors)
		if a.API.Completed > 0 {
			fmt.Fprintf(b, "- Total provider-reported API time: **%.2f s** (sum); mean **%.2f s**\n", a.API.TotalSeconds, a.API.TotalSeconds/float64(a.API.Completed))
		}
		for _, k := range sortedCountKeys(a.API.Models) {
			fmt.Fprintf(b, "- Model `%s`: %d request starts\n", md(k), a.API.Models[k])
		}
		for _, k := range sortedCountKeys(a.API.Providers) {
			fmt.Fprintf(b, "- Provider `%s`: %d request starts\n", md(k), a.API.Providers[k])
		}
	}
	if a.Stream.Started+a.Stream.Ended > 0 {
		fmt.Fprintf(b, "\n## Streaming observations\n\n- Stream starts / ends: **%d / %d**\n- Streams containing reasoning / text: **%d / %d**\n- Reasoning / text chunks: **%d / %d**\n- Maximum observed inter-chunk gap: **%s**\n", a.Stream.Started, a.Stream.Ended, a.Stream.WithReasoning, a.Stream.WithText, a.Stream.ReasoningDeltas, a.Stream.TextDeltas, formatMS(a.Stream.MaxGapMS))
		fmt.Fprintf(b, "\n| Stream | Observed duration | First reasoning | Last reasoning | First text | Last text | Reasoning chunks | Text chunks | Max gap |\n|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
		for i, r := range a.Stream.Records {
			fmt.Fprintf(b, "| %d | %s | %s | %s | %s | %s | %d | %d | %s |\n", i+1, formatMS(r.DurationMS), fmtMaybeMS(r.FirstReasoning), fmtMaybeMS(r.LastReasoning), fmtMaybeMS(r.FirstText), fmtMaybeMS(r.LastText), r.ReasoningDeltas, r.TextDeltas, formatMS(r.MaxGapMS))
		}
		fmt.Fprintf(b, "\n**Interpretation:** First/last chunks are relative to stream start and observed asynchronously. Missing last-chunk fields are reported as n/a. These timestamps do not measure private internal reasoning. Stream-to-API correlation is not assumed without an explicit request ID.\n")
	}
}

func allToolNames(transcript map[string]int, telemetry map[string]*ToolStat) []string {
	set := map[string]bool{}
	for name := range transcript {
		set[name] = true
	}
	for name := range telemetry {
		set[name] = true
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		ti, tj := 0.0, 0.0
		if telemetry[names[i]] != nil {
			ti = telemetry[names[i]].TotalMS
		}
		if telemetry[names[j]] != nil {
			tj = telemetry[names[j]].TotalMS
		}
		if ti == tj {
			return transcript[names[i]] > transcript[names[j]]
		}
		return ti > tj
	})
	return names
}

// findIntDirect inspects a single record, not the session's aggregate usage.
func findIntDirect(m map[string]any, keys ...string) *int64 {
	for _, key := range keys {
		if n, ok := number(m[key]); ok {
			v := int64(n)
			return &v
		}
	}
	return nil
}

func structuredText(v any, depth int) string {
	if depth > 7 || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		// Reasoning details may be JSON-encoded in the session store.
		t := strings.TrimSpace(x)
		if strings.HasPrefix(t, "[") || strings.HasPrefix(t, "{") {
			var parsed any
			if json.Unmarshal([]byte(t), &parsed) == nil {
				return structuredText(parsed, depth+1)
			}
		}
		return x
	case []any:
		parts := []string{}
		for _, part := range x {
			t := structuredText(part, depth+1)
			if strings.TrimSpace(t) != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		// Restrict to text-bearing fields; ignore provider signatures, IDs, and binary payloads.
		for _, key := range []string{"text", "reasoning_text", "reasoning", "content", "summary", "parts"} {
			if val, ok := x[key]; ok {
				if t := structuredText(val, depth+1); strings.TrimSpace(t) != "" {
					return t
				}
			}
		}
	}
	return ""
}

func extractMessageContent(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if items, ok := asSlice(v); ok {
		parts := []string{}
		for _, item := range items {
			if str, ok := item.(string); ok {
				parts = append(parts, str)
				continue
			}
			if m, ok := asMap(item); ok {
				if t := structuredText(m, 0); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	if m, ok := asMap(v); ok {
		return structuredText(m, 0)
	}
	return ""
}

func extractReasoning(m map[string]any) (text, origin string, present bool) {
	// Hermes can keep multiple mirrors of the same reasoning; prefer a full
	// string carrier, then the structured carriers. Never concatenate mirrors.
	for _, key := range []string{"reasoning", "reasoning_content", "reasoning_details", "codex_reasoning_items"} {
		v, exists := m[key]
		if !exists || v == nil {
			continue
		}
		present = true
		t := structuredText(v, 0)
		if strings.TrimSpace(t) != "" {
			return t, key, true
		}
	}
	return "", "", present
}

func extractTranscript(messages []any) []TranscriptMessage {
	result := make([]TranscriptMessage, 0, len(messages))
	for i, raw := range messages {
		m, ok := asMap(raw)
		if !ok {
			continue
		}
		active, exists := m["active"].(bool)
		if !exists {
			active = true
		}
		compacted, _ := m["compacted"].(bool)
		r := TranscriptMessage{
			Index: i + 1, Role: valueOr(getString(m, "role"), "unknown"),
			Active: active, Compacted: compacted,
			Content:      extractMessageContent(m["content"]),
			ToolName:     getString(m, "tool_name", "name"),
			FinishReason: getString(m, "finish_reason"),
			TokenCount:   findIntDirect(m, "token_count"),
		}
		if t, ok := parseTime(m["timestamp"]); ok {
			r.Timestamp = &t
		}
		if strings.EqualFold(r.Role, "assistant") {
			r.Reasoning, r.ReasoningSource, r.ReasoningSidecarPresent = extractReasoning(m)
		}
		for _, rawCall := range decodeToolCalls(m["tool_calls"]) {
			name := toolCallName(rawCall)
			if name == "" {
				name = "unknown"
			}
			r.ToolCalls = append(r.ToolCalls, name)
		}
		result = append(result, r)
	}
	return result
}

func fmtTokenCell(n *int64) string {
	if n == nil {
		return "n/a"
	}
	return strconv.FormatInt(*n, 10)
}

func appendAPIUsageMarkdown(b *strings.Builder, a *Analysis) {
	if len(a.APIUsageRows) == 0 {
		return
	}
	fmt.Fprint(b, "\n### Per-request provider usage (when supplied)\n\n")
	fmt.Fprint(b, "| Request | Model | API time | Input | Output | Reasoning | Cache read | Cache write | Total |\n|---:|---|---:|---:|---:|---:|---:|---:|---:|\n")
	hasUsage := 0
	for i, r := range a.APIUsageRows {
		if r.PromptTokens != nil || r.CompletionTokens != nil || r.TotalTokens != nil {
			hasUsage++
		}
		duration := "n/a"
		if r.DurationSeconds != nil {
			duration = fmt.Sprintf("%.2fs", *r.DurationSeconds)
		}
		fmt.Fprintf(b, "| %d | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			i+1, md(valueOr(r.Model, "n/a")), duration,
			fmtTokenCell(r.PromptTokens), fmtTokenCell(r.CompletionTokens),
			fmtTokenCell(r.ReasoningTokens), fmtTokenCell(r.CacheReadTokens),
			fmtTokenCell(r.CacheWriteTokens), fmtTokenCell(r.TotalTokens))
	}
	fmt.Fprintf(b, "\nProvider usage counters present on %d/%d completed API events. 'n/a' means not reported, not zero. Input and cache categories may overlap depending on the provider; reasoning can be a subset of output. No speculative fresh-token total is computed.\n", hasUsage, len(a.APIUsageRows))
}

// A content fence longer than any run of backticks in the source prevents
// Markdown headings, tables and embedded code from escaping the transcript.
func fencedTranscript(s string) string {
	fence := "````"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	return fence + "text\n" + s + "\n" + fence + "\n"
}

func timestampDelta(d time.Duration) string {
	if d == 0 {
		return "0 ms"
	}
	return formatMS(float64(d) / float64(time.Millisecond))
}

func firstUserAssistantStoredGap(messages []TranscriptMessage) *time.Duration {
	var user *time.Time
	for _, m := range messages {
		if !m.Active {
			continue
		}
		if user == nil && strings.EqualFold(m.Role, "user") && m.Timestamp != nil {
			user = m.Timestamp
			continue
		}
		if user != nil && strings.EqualFold(m.Role, "assistant") && m.Timestamp != nil && !m.Timestamp.Before(*user) {
			d := m.Timestamp.Sub(*user)
			return &d
		}
	}
	return nil
}

func appendTranscriptMarkdown(b *strings.Builder, a *Analysis) {
	fmt.Fprint(b, "\n## Stored conversation transcript\n\n")
	fmt.Fprint(b, "Content is read from the Hermes session export, not from the timing plugin. Automated `sync` requests `--redact`, but external/manual exports may be unredacted. This includes saved assistant reasoning only if the export actually contains it. Timestamps are message-persistence timestamps, **not** provider first-token timestamps or a private reasoning stopwatch. Tool result bodies and tool arguments are omitted here.\n")
	if len(a.Transcript) == 0 {
		fmt.Fprint(b, "\n_No stored messages available._\n")
		return
	}
	var previous *time.Time
	typedCount := map[string]int{}
	typedTokens := map[string]int64{}
	var earliest, latest *time.Time
	var reasonCount, textCount int
	for _, m := range a.Transcript {
		if m.TokenCount != nil && m.Active {
			typedCount[m.Role]++
			typedTokens[m.Role] += *m.TokenCount
		}
		if m.Reasoning != "" {
			reasonCount++
		}
		if strings.EqualFold(m.Role, "assistant") && m.Content != "" {
			textCount++
		}
		if m.Timestamp != nil && m.Active {
			if earliest == nil || m.Timestamp.Before(*earliest) {
				earliest = m.Timestamp
			}
			if latest == nil || m.Timestamp.After(*latest) {
				latest = m.Timestamp
			}
		}
	}
	if earliest != nil && latest != nil {
		fmt.Fprintf(b, "\n- First active recorded message: `%s`\n- Last active recorded message: `%s`\n- Active-message timestamp span: **%s** (stored message events, not session closure)\n", formatTime(earliest), formatTime(latest), timestampDelta(latest.Sub(*earliest)))
	}
	if gap := firstUserAssistantStoredGap(a.Transcript); gap != nil {
		fmt.Fprintf(b, "- First active user-to-assistant stored-message gap: **%s** (not a model thinking measurement)\n", timestampDelta(*gap))
	}
	fmt.Fprintf(b, "- Assistant messages with saved text: **%d**; with recoverable reasoning text: **%d**\n", textCount, reasonCount)
	if len(typedCount) > 0 {
		fmt.Fprint(b, "- Stored `token_count` by role (only active messages where provided): ")
		roles := make([]string, 0, len(typedCount))
		for role := range typedCount {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		parts := []string{}
		for _, role := range roles {
			parts = append(parts, fmt.Sprintf("%s %d across %d messages", role, typedTokens[role], typedCount[role]))
		}
		fmt.Fprintln(b, strings.Join(parts, "; "))
		fmt.Fprint(b, "  These stored per-message counts are not a decomposition of API input tokens: each request may resend system instructions, history, tool results, and cached context.\n")
	} else {
		fmt.Fprint(b, "- Stored per-message token counts: unavailable.\n")
	}
	fmt.Fprint(b, "\n### Message timeline and content\n")
	for _, m := range a.Transcript {
		if !m.Active {
			previous = nil
		} // archived rows may be out of time order
		heading := strings.ToUpper(m.Role)
		if m.ToolName != "" {
			heading += ": " + m.ToolName
		}
		fmt.Fprintf(b, "\n#### %d. %s\n\n", m.Index, md(heading))
		props := []string{}
		if m.Timestamp != nil {
			props = append(props, "at `"+formatTime(m.Timestamp)+"`")
			if previous != nil && !m.Timestamp.Before(*previous) {
				props = append(props, "gap since previous timestamp "+timestampDelta(m.Timestamp.Sub(*previous)))
			}
			previous = m.Timestamp
		}
		if !m.Active {
			props = append(props, "archived/inactive")
		}
		if m.Compacted {
			props = append(props, "compacted")
		}
		if m.TokenCount != nil {
			props = append(props, "stored token_count "+fmtTokenCell(m.TokenCount))
		}
		if m.FinishReason != "" {
			props = append(props, "finish_reason `"+md(m.FinishReason)+"`")
		}
		if len(props) > 0 {
			fmt.Fprintf(b, "%s.\n\n", strings.Join(props, "; "))
		}
		if len(m.ToolCalls) > 0 {
			fmt.Fprintf(b, "Tool calls issued: `%s` (arguments omitted).\n\n", md(strings.Join(m.ToolCalls, "`, `")))
		}
		if strings.EqualFold(m.Role, "tool") {
			fmt.Fprint(b, "_Tool result body omitted from this report; see redacted session export._\n")
			continue
		}
		if m.Content != "" {
			fmt.Fprint(b, "**Content**\n\n", fencedTranscript(m.Content), "\n")
		}
		if strings.EqualFold(m.Role, "assistant") {
			if m.Reasoning != "" {
				fmt.Fprintf(b, "**Saved reasoning** (field `%s`)\n\n%s\n", m.ReasoningSource, fencedTranscript(m.Reasoning))
			} else if m.ReasoningSidecarPresent {
				fmt.Fprint(b, "_A reasoning sidecar exists, but it contains no extractable text._\n")
			} else {
				fmt.Fprint(b, "_No saved reasoning field for this message._\n")
			}
		} else if m.Content == "" && len(m.ToolCalls) == 0 {
			fmt.Fprint(b, "_No printable message content._\n")
		}
	}
	fmt.Fprint(b, "\n**Privacy:** Raw prompts, assistant outputs, and saved reasoning can include personal or project-sensitive information even after `--redact`. Review the report before sharing it.\n")
}

func renderMarkdown(a *Analysis) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Hermes Task Report\n\n**Task:** %s  \n**Session:** `%s`  \n**Model:** %s  \n**Provider:** %s  \n**Source:** %s  \n**Working directory:** `%s`\n\n", md(a.Title), md(a.SessionID), md(valueOr(a.Model, "n/a")), md(valueOr(displayProvider(a), "n/a")), md(valueOr(a.Source, "n/a")), md(valueOr(a.CWD, "n/a")))
	fmt.Fprintf(&b, "## Summary\n\n| Metric | Result |\n|---|---:|\n")
	fmt.Fprintf(&b, "| Recorded session span | %s |\n| Active messages | %d |\n| Archived messages | %d |\n| Active tool calls | %d |\n| Archived tool calls | %d |\n| Input tokens | %s |\n| Output tokens | %s |\n| Cache-read tokens | %s |\n| Cache-write tokens | %s |\n| Reasoning tokens | %s |\n| Cost (USD) | %s |\n", formatDuration(durationFrom(a.Started, a.Ended, a.WallMS)), a.ActiveMessages, a.ArchivedMessages, a.ActiveToolCalls, a.ArchivedToolCalls, formatInt(a.InputTokens), formatInt(a.OutputTokens), formatInt(a.CacheReadTokens), formatInt(a.CacheWriteTokens), formatInt(a.ReasoningTokens), formatFloat(a.CostUSD, ""))
	names := allToolNames(a.ToolNames, a.ToolStats)
	if len(names) > 0 {
		fmt.Fprintf(&b, "\n## Tool activity\n\n| Tool | Transcript calls | Telemetry calls | Total time | Average | Result bytes | Confirmed errors | Unknown outcomes |\n|---|---:|---:|---:|---:|---:|---:|---:|\n")
		for _, name := range names {
			s := a.ToolStats[name]
			if s == nil {
				s = &ToolStat{}
			}
			fmt.Fprintf(&b, "| `%s` | %d | %d | %s | %s | %s | %d | %d |\n", md(name), a.ToolNames[name], s.Calls, formatMS(s.TotalMS), formatMS(s.TotalMS/float64(max(1, s.Calls))), humanBytes(s.ResultBytes), s.Failures, s.Unknown)
		}
		if len(a.ToolStats) > 0 {
			fmt.Fprintf(&b, "\n- Accumulated tool execution: **%s** (sum of durations; parallel calls overlap).\n- Estimated union of tool-running intervals: **%s**.\n- Total result payload: **%s** / %d characters. This is returned tool payload size, not actual LLM context tokens.\n", formatMS(a.TelemetryToolMS), formatMS(a.TelemetryToolUnionMS), humanBytes(a.TelemetryResultBytes), a.TelemetryResultChars)
		}
	}
	appendAPIMarkdown(&b, a)
	appendAPIUsageMarkdown(&b, a)
	appendWorkloadMarkdown(&b, a)
	if a.TurnCount > 0 {
		fmt.Fprintf(&b, "\n## Turn timing\n\n- Completed measured turns: %d\n- Sum of turn durations: %.2f seconds\n- Longest turn: %.2f seconds\n", a.TurnCount, a.TurnDurationTotalMS/1000, a.TurnDurationMaxMS/1000)
	}
	if a.Started != nil || a.Ended != nil {
		fmt.Fprintf(&b, "\n## Timestamps\n\n- Started: `%s`\n- Ended: `%s`\n", formatTime(a.Started), formatTime(a.Ended))
		if ms, source, ok := kickoffDelay(a); ok {
			fmt.Fprintf(&b, "- First %s: **%s** after the first stored user message timestamp (not an exact prompt submission-to-provider measurement).\n", source, formatKickoff(ms))
		}
		if other, tools, visible, ok := observedTimePartition(a); ok {
			fmt.Fprintf(&b, "- Observed turn partition: **%.2f s** other processing/waiting, **%.2f s** tool-busy, **%.2f s** streamed visible text (overlapping intervals counted once). The remainder is **not** a measured internal thinking time.\n", other/1000, tools/1000, visible/1000)
		}
	}
	if a.LargestGapMS != nil {
		fmt.Fprintf(&b, "- Largest message timestamp gap: **%s**\n", formatMS(*a.LargestGapMS))
	}
	if len(a.TopGaps) > 0 {
		fmt.Fprintf(&b, "\n| Message gap | Previous role | Next role |\n|---:|---|---|\n")
		for _, g := range a.TopGaps {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", formatMS(g.GapMS), md(valueOr(g.FromRole, "?")), md(valueOr(g.ToRole, "?")))
		}
	}
	fmt.Fprintf(&b, "\n## Data integrity\n\n- Session export SHA-256: `%s`\n", a.SessionSHA256)
	if a.TelemetrySHA256 != "" {
		fmt.Fprintf(&b, "- Telemetry log SHA-256: `%s`\n", a.TelemetrySHA256)
	} else {
		fmt.Fprintf(&b, "- Telemetry log: unavailable\n")
	}
	fmt.Fprintf(&b, "- Matched telemetry events: %d\n- Tool starts / completions: %d / %d\n- Unattributed tool events excluded: %d\n- Malformed telemetry lines: %d\n", a.TelemetryEvents, a.TelemetryStarted, a.TelemetryCompleted, a.TelemetryUnattributed, a.TelemetryBadLines)
	if a.TimingComplete != nil && !*a.TimingComplete {
		fmt.Fprintf(&b, "\n**Timing caveat:** Hermes' exported timing summary is derived from message timestamps and marked incomplete; it is not an active-work stopwatch.\n")
	}
	if len(a.Warnings) > 0 {
		fmt.Fprintf(&b, "\n## Notes\n\n")
		for _, w := range uniqueStrings(a.Warnings) {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}
	appendTranscriptMarkdown(&b, a)
	fmt.Fprintf(&b, "\n---\nGenerated by `hermes-bench`. Deterministic local analysis; no LLM summarization.\n")
	return b.String()
}

// The latest report for a task has a stable human-friendly title, while
// per-session copies use a short hash so repeated runs never overwrite history.
func reportFilename(c SessionCandidate) string {
	return safeReportTitle(c.Title) + ".md"
}

func historyReportFilename(c SessionCandidate) string {
	sum := sha256.Sum256([]byte(c.ID))
	return fmt.Sprintf("%s (%x).md", safeReportTitle(c.Title), sum[:6])
}

func safeReportTitle(title string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(title) {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			b.WriteRune('-')
		} else {
			b.WriteRune(r)
		}
	}
	name := strings.TrimRight(strings.TrimSpace(b.String()), ". ")
	if name == "" {
		name = "Untitled Hermes session"
	}
	if len([]rune(name)) > 110 {
		name = string([]rune(name)[:110])
	}
	switch strings.ToUpper(name) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		name = "Task " + name
	}
	return name
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "session"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}
func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
func withTrailingSeparator(p string) string {
	if strings.HasSuffix(p, string(os.PathSeparator)) {
		return p
	}
	return p + string(os.PathSeparator)
}
func fileState(p string) string {
	if _, err := os.Stat(p); err == nil {
		return "found"
	}
	return "not found"
}
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
func valueOr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
func formatTime(t *time.Time) string {
	if t == nil {
		return "unavailable"
	}
	return t.Local().Format(time.RFC3339)
}
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "unavailable"
	}
	s := int64(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %02dm %02ds", s/3600, (s%3600)/60, s%60)
}
func formatMS(v float64) string {
	if v <= 0 {
		return "—"
	}
	if v >= 1000 {
		return fmt.Sprintf("%.2f s", v/1000)
	}
	return fmt.Sprintf("%.0f ms", v)
}
func formatInt(v *int64) string {
	if v == nil {
		return "n/a"
	}
	return strconv.FormatInt(*v, 10)
}
func formatFloat(v *float64, suffix string) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.4f%s", *v, suffix)
}
func humanBytes(n int64) string {
	if n < 0 {
		return "n/a"
	}
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}
func md(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}
func sortedToolNames(m map[string]*ToolStat) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if m[names[i]].TotalMS == m[names[j]].TotalMS {
			return names[i] < names[j]
		}
		return m[names[i]].TotalMS > m[names[j]].TotalMS
	})
	return names
}
func uniqueStrings(xs []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func unwrapSession(obj map[string]any) map[string]any {
	if nested, ok := asMap(obj["session"]); ok {
		merged := cloneMap(nested)
		for k, v := range obj {
			if k != "session" {
				merged[k] = v
			}
		}
		return merged
	}
	return obj
}
func readJSONLObjects(path string) ([]map[string]any, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	var rows []map[string]any
	bad := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			bad++
			continue
		}
		rows = append(rows, m)
	}
	if err := scanner.Err(); err != nil {
		return rows, bad, fmt.Errorf("read %s: %w", path, err)
	}
	if len(rows) == 0 {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, bad, err
		}
		var v any
		if json.Unmarshal(data, &v) == nil {
			if m, ok := asMap(v); ok {
				rows = append(rows, m)
			}
			if xs, ok := asSlice(v); ok {
				for _, x := range xs {
					if m, ok := asMap(x); ok {
						rows = append(rows, m)
					}
				}
			}
		}
	}
	return rows, bad, nil
}
func decodeToolCalls(v any) []any {
	if s, ok := v.(string); ok {
		var d any
		if json.Unmarshal([]byte(s), &d) == nil {
			v = d
		}
	}
	if xs, ok := asSlice(v); ok {
		return xs
	}
	return nil
}
func toolCallName(v any) string {
	m, ok := asMap(v)
	if !ok {
		return ""
	}
	if f, ok := asMap(m["function"]); ok {
		return getString(f, "name")
	}
	return getString(m, "name", "tool_name")
}
func asMap(v any) (map[string]any, bool) { m, ok := v.(map[string]any); return m, ok }
func asSlice(v any) ([]any, bool)        { s, ok := v.([]any); return s, ok }
func cloneMap(m map[string]any) map[string]any {
	r := map[string]any{}
	for k, v := range m {
		r[k] = v
	}
	return r
}
func first(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}
func getString(m map[string]any, keys ...string) string {
	v := first(m, keys...)
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, e := x.Float64()
		return f, e == nil
	case string:
		f, e := strconv.ParseFloat(x, 64)
		return f, e == nil
	}
	return 0, false
}
func findInt(m map[string]any, keys []string) *int64 {
	containers := []map[string]any{m}
	for _, k := range []string{"usage", "token_usage", "usage_totals", "totals"} {
		if x, ok := asMap(m[k]); ok {
			containers = append(containers, x)
		}
	}
	for _, c := range containers {
		for _, k := range keys {
			if n, ok := number(c[k]); ok {
				v := int64(n)
				return &v
			}
		}
	}
	return nil
}
func findFloat(m map[string]any, keys []string) *float64 {
	for _, k := range keys {
		if n, ok := number(m[k]); ok {
			return &n
		}
	}
	return nil
}
func parseTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999-07:00", "2006-01-02 15:04:05"} {
			if t, e := time.Parse(layout, x); e == nil {
				return t, true
			}
		}
	case float64:
		if x > 1e12 {
			return time.UnixMilli(int64(x)).UTC(), true
		}
		if x > 1e9 {
			return time.Unix(int64(x), int64((x-float64(int64(x)))*1e9)).UTC(), true
		}
	case int64:
		if x > 1e12 {
			return time.UnixMilli(x).UTC(), true
		}
		if x > 1e9 {
			return time.Unix(x, 0).UTC(), true
		}
	}
	return time.Time{}, false
}
func fileSHA256(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func durationFrom(start, end *time.Time, wall *float64) time.Duration {
	if start != nil && end != nil && !end.Before(*start) {
		return end.Sub(*start)
	}
	if wall != nil {
		return time.Duration(*wall) * time.Millisecond
	}
	return 0
}
