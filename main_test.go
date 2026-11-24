package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateCountsActiveAndArchivedToolCalls(t *testing.T) {
	m := map[string]any{
		"id": "s1", "title": "Example", "started_at": "2026-11-22T02:00:00Z",
		"messages": []any{
			map[string]any{"role": "assistant", "active": true, "tool_calls": []any{map[string]any{"function": map[string]any{"name": "read_file"}}}},
			map[string]any{"role": "assistant", "active": false, "tool_calls": []any{map[string]any{"function": map[string]any{"name": "search_files"}}}},
		},
	}
	c := candidateFromSession(m, "session.jsonl")
	if c.ToolCalls != 2 || c.ActiveToolCalls != 1 || c.ArchivedToolCalls != 1 {
		t.Fatalf("wrong tool counts: %+v", c)
	}
}

func TestTelemetryMatchesSessionAndExcludesUnattributed(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	eventsPath := filepath.Join(dir, "events.jsonl")
	put(t, sessionPath, `{"id":"s1","title":"Test","started_at":"2026-11-22T02:00:00Z","ended_at":"2026-11-22T02:00:10Z","messages":[{"role":"assistant","active":true,"tool_calls":[{"id":"c1","function":{"name":"read_file"}}]}],"input_tokens":100,"output_tokens":20,"timings":{"complete":false,"largest_gap_ms":4000,"intervals":[{"gap_ms":4000,"from_role":"assistant","to_role":"tool"}]}}`+"\n")
	put(t, eventsPath, strings.Join([]string{
		`{"event":"plugin.loaded","version":"x"}`,
		`{"event":"tool.started","session_id":"s1","tool":"read_file","ts":"2026-11-22T02:00:01Z"}`,
		`{"event":"tool.completed","session_id":"s1","tool":"read_file","ts":"2026-11-22T02:00:02.250Z","duration_ms":1250,"result_bytes":500,"result_chars":480}`,
		`{"event":"tool.completed","tool":"terminal","ts":"2026-11-22T02:00:04Z","duration_ms":100}`,
		`{"event":"tool.completed","session_id":"other","tool":"terminal","ts":"2026-11-22T02:00:05Z","duration_ms":100}`,
	}, "\n")+"\n")
	objects, _, err := readJSONLObjects(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	c := candidateFromSession(unwrapSession(objects[0]), sessionPath)
	a, err := analyze(c, eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	if a.TelemetryCompleted != 1 || a.TelemetryStarted != 1 {
		t.Fatalf("unexpected telemetry counts: starts=%d completed=%d", a.TelemetryStarted, a.TelemetryCompleted)
	}
	if a.TelemetryUnattributed != 1 {
		t.Fatalf("unattributed=%d, want 1", a.TelemetryUnattributed)
	}
	if a.TelemetryResultBytes != 500 {
		t.Fatalf("result bytes=%d, want 500", a.TelemetryResultBytes)
	}
	if a.TelemetryToolUnionMS != 1250 {
		t.Fatalf("union=%v, want 1250", a.TelemetryToolUnionMS)
	}
	if a.ActiveToolCalls != 1 || a.ArchivedToolCalls != 0 {
		t.Fatalf("active/archive count wrong: %+v", a)
	}
	if a.TimingComplete == nil || *a.TimingComplete {
		t.Fatal("expected incomplete Hermes timing marker")
	}
}

func TestDiscoveryPairsBySessionIDAndSortsNewest(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	put(t, filepath.Join(sessions, "older.jsonl"), `{"id":"old","title":"Older task","started_at":"2026-11-20T00:00:00Z","messages":[]}`+"\n")
	put(t, filepath.Join(sessions, "newer.jsonl"), `{"id":"new","title":"Newer task","started_at":"2026-11-22T00:00:00Z","messages":[]}`+"\n")
	telemetry := filepath.Join(dir, "events.jsonl")
	put(t, telemetry, `{"event":"tool.completed","session_id":"new","tool":"read_file","duration_ms":8}`+"\n")
	cfg := Config{SessionsDir: sessions, TelemetryFile: telemetry, SessionPattern: "*.jsonl"}
	cs, _, err := discoverSessions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].ID != "new" || cs[0].TelemetryTools != 1 || cs[1].ID != "old" || cs[1].TelemetryTools != 0 {
		t.Fatalf("unexpected listing: %+v", cs)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	cfg := Config{SessionsDir: filepath.Join(dir, "sessions"), TelemetryFile: filepath.Join(dir, "events.jsonl"), ReportsDir: filepath.Join(dir, "reports"), SessionPattern: "*.jsonl"}
	b, _ := json.Marshal(cfg)
	put(t, p, string(b))
	loaded, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SessionsDir != cfg.SessionsDir || loaded.TelemetryFile != cfg.TelemetryFile {
		t.Fatalf("config mismatch: %+v", loaded)
	}
}

func TestIntervalUnionMergesParallelCalls(t *testing.T) {
	base := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	got := intervalUnion([]Interval{{Start: base, End: base.Add(2 * time.Second)}, {Start: base.Add(time.Second), End: base.Add(3 * time.Second)}, {Start: base.Add(4 * time.Second), End: base.Add(5 * time.Second)}})
	if got != 4000 {
		t.Fatalf("union=%v, want 4000ms", got)
	}
}

func TestAPIAndStreamTelemetryWithLastChunkCompatibility(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.jsonl")
	eventsPath := filepath.Join(dir, "events.jsonl")
	put(t, sessionPath, `{"id":"session-new","title":"Timing","messages":[{"role":"user","content":"Inspect code"}]}`+"\n")
	put(t, eventsPath, strings.Join([]string{
		`{"event":"api.started","session_id":"session-new","model":"longcat","provider":"nous","ts":"2026-11-22T10:00:00Z"}`,
		`{"event":"api.completed","session_id":"session-new","api_duration":12.5,"ts":"2026-11-22T10:00:12Z"}`,
		`{"event":"stream.started","session_id":"session-new","ts":"2026-11-22T10:00:00Z"}`,
		`{"event":"stream.ended","session_id":"session-new","ts":"2026-11-22T10:00:12Z","observed_duration_ms":12000,"first_reasoning_ms":1000,"last_reasoning_ms":7000,"first_text_ms":8000,"last_text_ms":11500,"reasoning_deltas":90,"text_deltas":60,"max_delta_gap_ms":300}`,
		`{"event":"stream.started","session_id":"session-new","ts":"2026-11-22T10:01:00Z"}`,
		`{"event":"stream.ended","session_id":"session-new","ts":"2026-11-22T10:01:10Z","observed_duration_ms":10000,"first_reasoning_ms":1000,"reasoning_deltas":100,"text_deltas":0}`,
	}, "\n")+"\n")
	objects, _, err := readJSONLObjects(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	candidate := candidateFromSession(unwrapSession(objects[0]), sessionPath)
	a, err := analyze(candidate, eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	if a.API.Started != 1 || a.API.Completed != 1 || a.API.TotalSeconds != 12.5 {
		t.Fatalf("API: %+v", a.API)
	}
	if a.Stream.Started != 2 || a.Stream.Ended != 2 || a.Stream.WithReasoning != 2 || a.Stream.WithText != 1 {
		t.Fatalf("stream: %+v", a.Stream)
	}
	if len(a.Stream.ObservedReasoningSpans) != 1 || a.Stream.ObservedReasoningSpans[0] != 6000 {
		t.Fatalf("spans: %+v", a.Stream.ObservedReasoningSpans)
	}
	if len(a.Stream.Records) != 2 || a.Stream.Records[1].LastReasoning != nil {
		t.Fatal("missing last reasoning not preserved")
	}
	if a.TelemetryEvents != 6 {
		t.Fatalf("matched events %d", a.TelemetryEvents)
	}
	report := renderMarkdown(a)
	if !strings.Contains(report, "Last reasoning") || !strings.Contains(report, "n/a") || !strings.Contains(report, "12.50 s") {
		t.Fatal(report)
	}
}

func TestIncrementalSessionDetectionAndStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "events.jsonl")
	put(t, events, strings.Join([]string{
		`{"event":"turn.started","session_id":"old","ts":"2026-11-22T10:00:00Z"}`,
		`{"event":"tool.started","session_id":"new","ts":"2026-11-22T11:00:00Z"}`,
		`{"event":"stream.ended","session_id":"new","ts":"2026-11-22T11:00:20Z"}`,
		`{"event":"plugin.loaded","ts":"2026-11-22T11:00:30Z"}`,
	}, "\n")+"\n")
	sessions, err := collectTelemetrySessions(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions["new"].Count != 2 || !sessions["new"].Last.After(sessions["old"].Last) {
		t.Fatalf("unexpected sessions: %+v", sessions)
	}
	path := filepath.Join(dir, "state.json")
	if err := saveSyncState(path, syncState{Counts: map[string]int{"new": 2}}); err != nil {
		t.Fatal(err)
	}
	state, exists, err := readSyncState(path)
	if err != nil || !exists || state.Counts["new"] != 2 {
		t.Fatalf("read state: %+v, %v, %v", state, exists, err)
	}
	if syncExportPath(dir, "new") == syncExportPath(dir, "old") {
		t.Fatal("export paths collided")
	}
}

func TestLegacyReportsStillReadableWithIncrementalSnapshots(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	put(t, filepath.Join(sessions, "hermes-bench-synced.jsonl"), `{"id":"older","title":"Older","started_at":"2026-11-22T10:00:00Z","messages":[]}`+"\n")
	put(t, syncExportPath(sessions, "newest"), `{"id":"newest","title":"New","started_at":"2026-11-22T11:00:00Z","messages":[]}`+"\n")
	telemetry := filepath.Join(dir, "events.jsonl")
	put(t, telemetry, `{"event":"turn.started","session_id":"newest"}`+"\n")
	cfg := Config{SessionsDir: sessions, TelemetryFile: telemetry, SessionPattern: "*.jsonl"}
	candidates, _, err := discoverSessions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].ID != "newest" {
		t.Fatalf("candidates: %+v", candidates)
	}
}

func TestConciseConsoleSummaryGroundedNumbers(t *testing.T) {
	intPtr := func(n int64) *int64 { return &n }
	floatPtr := func(n float64) *float64 { return &n }
	base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	firstAPI := base.Add(8 * time.Millisecond)
	a := &Analysis{
		Title:       "Inspect project configuration loading process",
		Model:       "tencent/hy4-flash:free",
		InputTokens: intPtr(46104), OutputTokens: intPtr(3301),
		ReasoningTokens: intPtr(1224), CacheReadTokens: intPtr(218112),
		CostUSD:             floatPtr(0),
		SessionUserMessages: 1, FirstAPIStart: &firstAPI,
		Transcript: []TranscriptMessage{
			{Role: "user", Active: true, Timestamp: &base, Content: "Inspect this project's configuration loading process. Identify the relevant files and explain how configuration is passed through the application. Do not modify anything."},
			{Role: "assistant", Active: true, Content: strings.Repeat("A", 3800), FinishReason: "stop"},
		},
		TurnCount: 1, TurnDurationTotalMS: 82990,
		ActiveToolCalls: 23, TelemetryStarted: 23, TelemetryCompleted: 23,
		TelemetryToolMS: 37060, TelemetryToolUnionMS: 17580,
		API:    APIStat{Started: 9, Completed: 9, TotalSeconds: 64.32, Providers: map[string]int{"nous": 9}},
		Stream: StreamStat{Started: 9, Ended: 9},
		ToolStats: map[string]*ToolStat{
			"search_files": {Calls: 7, TotalMS: 18520, ResultBytes: 25500, ResultChars: 24800},
			"read_file":    {Calls: 13, TotalMS: 16420, ResultBytes: 70451, ResultChars: 68400},
			"terminal":     {Calls: 1, TotalMS: 1790, ResultBytes: 2662, ResultChars: 2480},
			"skill_view":   {Calls: 2, TotalMS: 330, ResultBytes: 11264, ResultChars: 11000},
		},
		APIUsageRows: []APIRow{
			{PromptTokens: intPtr(11945), CacheReadTokens: intPtr(640), CompletionTokens: intPtr(176)},
			{PromptTokens: intPtr(15570), CacheReadTokens: intPtr(11776), CompletionTokens: intPtr(264)},
			{PromptTokens: intPtr(19557), CacheReadTokens: intPtr(15360), CompletionTokens: intPtr(214)},
			{PromptTokens: intPtr(23826), CacheReadTokens: intPtr(19328), CompletionTokens: intPtr(206)},
			{PromptTokens: intPtr(28667), CacheReadTokens: intPtr(23680), CompletionTokens: intPtr(169)},
			{PromptTokens: intPtr(36135), CacheReadTokens: intPtr(28416), CompletionTokens: intPtr(261)},
			{PromptTokens: intPtr(39874), CacheReadTokens: intPtr(35968), CompletionTokens: intPtr(228)},
			{PromptTokens: intPtr(43461), CacheReadTokens: intPtr(39680), CompletionTokens: intPtr(199)},
			{PromptTokens: intPtr(45181), CacheReadTokens: intPtr(43264), CompletionTokens: intPtr(1584)},
		},
	}
	got := renderConsoleSummary(a, `C:\bench\reports\Inspect project configuration loading process.md`)
	for _, want := range []string{
		"hy4-flash completed the task in 83.0s (8ms before first model request; 23 tool calls across 9 API calls; $0.0000)",
		"spending 17.6s waiting on tools (37.1s total across individual tool calls)",
		"search_files", "18.52s | ~6.2k toks",
		"read_file", "16.42s | ~17.1k toks",
		"terminal", "1.79s | ~620 toks",
		"skill_view", "330ms | ~2.8k toks",
		"prompt toks (x9", "background workload toks", "final-answer toks",
		"264.2k total input (46.1k uncached, 218.1k cached)",
		"3.3k output (1.2k reported reasoning; not added again)",
		"Telemetry matches.",
		`Full report: C:\bench\reports\Inspect project configuration loading process.md`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in console summary:\n%s", want, got)
		}
	}
	if prompt, final, background, total, n, ok := approximateWorkload(a); !ok || n != 9 || prompt*int64(n)+final+background != total || total != 267517 {
		t.Fatalf("invalid workload partition: prompt=%d, final=%d, bg=%d, total=%d, n=%d, ok=%v", prompt, final, background, total, n, ok)
	}
	report := renderMarkdown(a)
	if !strings.Contains(report, "Estimated prompt / background / final workload") || !strings.Contains(report, "Approximate tool-result text sizes") {
		t.Fatalf("Markdown lacks workload explanation: %s", report)
	}
	for _, bad := range []string{"spent 53.6s thinking", "50.6k fresh", "7.15s before first model request"} {
		if strings.Contains(got, bad) {
			t.Errorf("unsupported attribution %q in summary", bad)
		}
	}
}

func TestConciseConsoleSummaryMissingTelemetryAndMultipleTurns(t *testing.T) {
	a := &Analysis{Title: "Follow-up", Model: "my-model", SessionUserMessages: 3,
		TurnCount: 2, TurnDurationTotalMS: 45000, ActiveToolCalls: 2,
	}
	got := renderConsoleSummary(a, "")
	if !strings.Contains(got, "Multi-turn session: 3 user messages; totals are session-wide") ||
		!strings.Contains(got, "completed 2 turns in 45.0s combined") ||
		!strings.Contains(got, "Telemetry incomplete") ||
		!strings.Contains(got, "Token workload estimate: unavailable") {
		t.Fatalf("unexpected multi-turn summary:\n%s", got)
	}
	if strings.Contains(got, "$0.0000") || strings.Contains(got, "Full report:") {
		t.Fatal("missing values were invented")
	}
}

func TestTokenEstimateSkipsMetadataAndReasoning(t *testing.T) {
	p := func(n int64) *int64 { return &n }
	a := &Analysis{InputTokens: p(200), CacheReadTokens: p(50), OutputTokens: p(100), SessionUserMessages: 1,
		API: APIStat{Started: 2, Completed: 2},
		APIUsageRows: []APIRow{
			{PromptTokens: p(100), CacheReadTokens: p(20), CompletionTokens: p(40)},
			{PromptTokens: p(150), CacheReadTokens: p(30), CompletionTokens: p(60)},
		},
		Transcript: []TranscriptMessage{
			{Role: "user", Active: true, Content: "abcdefgh", Reasoning: strings.Repeat("sensitive", 1000)},
			{Role: "tool", Active: true, Content: strings.Repeat("secret", 1000)},
			{Role: "assistant", Active: true, Content: "12345678", Reasoning: strings.Repeat("private", 1000)},
		},
	}
	prompt, final, rest, total, calls, ok := approximateWorkload(a)
	if !ok || prompt != 2 || final != 2 || calls != 2 || total != 350 || rest != 344 {
		t.Fatalf("bad estimate: prompt %d final %d rest %d total %d calls %d ok %v", prompt, final, rest, total, calls, ok)
	}
	// Missing provider request usage means we cannot invent a residual.
	a.APIUsageRows = nil
	if _, _, _, _, _, ok := approximateWorkload(a); ok {
		t.Fatal("background inferred despite missing complete API usage")
	}
}

func TestStoredTranscriptReasoningAndTimestampGaps(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.jsonl")
	eventsPath := filepath.Join(dir, "events.jsonl")
	put(t, sessionPath, `{"id":"s-transcript","title":"Trace loading","input_tokens":100,"output_tokens":50,"messages":[`+
		`{"role":"user","content":"Inspect config loader","timestamp":1795348455.289708,"token_count":4,"active":true},`+
		`{"role":"assistant","content":"","timestamp":1795348462.745425,"reasoning":"Consider project entry point and config precedence.","reasoning_content":"Consider project entry point and config precedence.","token_count":5,"tool_calls":[{"function":{"name":"search_files"}}],"active":true},`+
		`{"role":"tool","tool_name":"search_files","content":"TOP_SECRET_TOOL_BODY","timestamp":1795348464.7440195,"token_count":7,"active":true},`+
		`{"role":"assistant","content":"Config is loaded by pp.ps1.","timestamp":1795348538.4756825,"reasoning_details":"[{\"type\":\"reasoning\",\"text\":\"Now summarize safely.\"}]","token_count":9,"finish_reason":"stop","active":true},`+
		`{"role":"user","content":"Old prompt from archive","timestamp":1795348200.0,"active":false,"compacted":true}`+
		`]}`+"\n")
	put(t, eventsPath, `{"event":"api.completed","session_id":"s-transcript","model":"hy4-flash","api_duration":7.2,"usage_prompt_tokens":101,"usage_completion_tokens":50,"usage_reasoning_tokens":10,"usage_cache_read_tokens":70,"usage_total_tokens":151}`+"\n")
	objects, _, err := readJSONLObjects(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	candidate := candidateFromSession(unwrapSession(objects[0]), sessionPath)
	a, err := analyze(candidate, eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Transcript) != 5 || a.Transcript[1].ReasoningSource != "reasoning" || a.Transcript[3].Reasoning != "Now summarize safely." {
		t.Fatalf("bad transcript extraction: %+v", a.Transcript)
	}
	if len(a.APIUsageRows) != 1 || a.APIUsageRows[0].PromptTokens == nil || *a.APIUsageRows[0].PromptTokens != 101 {
		t.Fatalf("bad usage: %+v", a.APIUsageRows)
	}
	report := renderMarkdown(a)
	for _, item := range []string{"Inspect config loader", "Consider project entry point", "Now summarize safely.", "Config is loaded by pp.ps1.", "7.46 s", "83.19 s", "stored token_count 9", "Per-request provider usage", "101", "70", "archived/inactive", "Tool result body omitted", "not a decomposition of API input tokens"} {
		if !strings.Contains(report, item) {
			t.Errorf("missing %q in report:\n%s", item, report)
		}
	}
	if strings.Contains(report, "TOP_SECRET_TOOL_BODY") {
		t.Fatal("tool body leaked into Markdown")
	}
	console := renderConsoleSummary(a, "")
	for _, sensitive := range []string{"Inspect config loader", "Consider project entry point", "Now summarize safely", "TOP_SECRET_TOOL_BODY"} {
		if strings.Contains(console, sensitive) {
			t.Fatalf("transcript content leaked into console: %q", sensitive)
		}
	}
}

func TestTranscriptMarkdownFenceAndMissingReasoning(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "This includes ```code``` and #### injected heading"},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Answer"}}, "active": true},
	}
	a := &Analysis{Title: "Example", ToolNames: map[string]int{}, ToolStats: map[string]*ToolStat{}, Transcript: extractTranscript(messages)}
	report := renderMarkdown(a)
	if !strings.Contains(report, "````text\nThis includes ```code```") || !strings.Contains(report, "No saved reasoning field") || !strings.Contains(report, "Answer") {
		t.Fatalf("bad Markdown transcript:\n%s", report)
	}
	if !strings.Contains(report, "Stored per-message token counts: unavailable") {
		t.Fatal("missing token-count caveat")
	}
}

func TestReadableFilenameAndHistoricalPaths(t *testing.T) {
	first := SessionCandidate{Title: "Inspect project configuration loading process", ID: "local-abc123"}
	second := SessionCandidate{Title: first.Title, ID: "local-def456"}
	if reportFilename(first) != "Inspect project configuration loading process.md" {
		t.Fatalf("wrong filename: %s", reportFilename(first))
	}
	if historyReportFilename(first) == historyReportFilename(second) {
		t.Fatal("distinct sessions must not use the same historical filename")
	}
	if safeReportTitle(`CON`) != "Task CON" {
		t.Fatal("unsafe reserved filename")
	}
	if safeReportTitle(`A/B:*?`) != "A-B---" {
		t.Fatal("unsafe characters not replaced")
	}
}

func TestLatestReportOverwritesAliasButPreservesHistory(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	reports := filepath.Join(dir, "reports")
	put(t, filepath.Join(sessions, "older.jsonl"), `{"id":"older","title":"Identical task","started_at":"2026-11-21T00:00:00Z","messages":[{"role":"user","content":"task"}]}`+"\n")
	put(t, filepath.Join(sessions, "newer.jsonl"), `{"id":"newer","title":"Identical task","started_at":"2026-11-22T00:00:00Z","messages":[{"role":"user","content":"task"}]}`+"\n")
	cfg := Config{SessionsDir: sessions, ReportsDir: reports, SessionPattern: "*.jsonl"}
	if err := reportSession(cfg, 2, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(reports, "Identical task.md")); !os.IsNotExist(err) {
		t.Fatalf("older report must not create latest alias: %v", err)
	}
	if err := reportSession(cfg, 1, true); err != nil {
		t.Fatal(err)
	}
	latest, err := os.ReadFile(filepath.Join(reports, "Identical task.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(latest), "**Session:** `newer`") {
		t.Fatal("latest alias points to wrong session")
	}
	if _, err := os.Stat(filepath.Join(reports, "history", historyReportFilename(SessionCandidate{Title: "Identical task", ID: "older"}))); err != nil {
		t.Fatal(err)
	}
}

func TestKickoffAndPartitionUsesIntervals(t *testing.T) {
	base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	prompt := base
	api := base.Add(7 * time.Millisecond)
	a := &Analysis{
		SessionUserMessages: 1,
		Transcript:          []TranscriptMessage{{Role: "user", Active: true, Timestamp: &prompt}},
		FirstAPIStart:       &api,
		TurnWindows:         []Interval{{Start: base, End: base.Add(83 * time.Second)}},
		ToolWindows:         []Interval{{Start: base.Add(10 * time.Second), End: base.Add(27 * time.Second)}, {Start: base.Add(18 * time.Second), End: base.Add(28 * time.Second)}},
		TextWindows:         []Interval{{Start: base.Add(76 * time.Second), End: base.Add(82 * time.Second)}},
	}
	ms, source, ok := kickoffDelay(a)
	if !ok || ms != 7 || source != "first model request" {
		t.Fatalf("kickoff=%.2f %q %v", ms, source, ok)
	}
	if formatKickoff(ms) != "7ms" {
		t.Fatal("kickoff formatting")
	}
	other, tools, textMS, ok := observedTimePartition(a)
	if !ok || other != 59_000 || tools != 18_000 || textMS != 6_000 {
		t.Fatalf("partition=%v %v %v %v", other, tools, textMS, ok)
	}
	// Overlapping answer and tool observations must never double-subtract.
	a.TextWindows = []Interval{{Start: base.Add(26 * time.Second), End: base.Add(32 * time.Second)}}
	other, tools, textMS, ok = observedTimePartition(a)
	if !ok || other != 61_000 || tools != 18_000 || textMS != 4_000 {
		t.Fatalf("overlap partition=%v %v %v %v", other, tools, textMS, ok)
	}
}

func TestVerifiedFreshTokenUsageReconcilesProvider(t *testing.T) {
	p := func(n int64) *int64 { return &n }
	a := &Analysis{InputTokens: p(46104), CacheReadTokens: p(218112), OutputTokens: p(3301), API: APIStat{Completed: 2}, APIUsageRows: []APIRow{
		{PromptTokens: p(100000), CacheReadTokens: p(80000), CompletionTokens: p(1000)},
		{PromptTokens: p(164216), CacheReadTokens: p(138112), CompletionTokens: p(2301)},
	}}
	total, fresh, ok := verifiedFreshTokenUsage(a)
	if !ok || total != 264216 || fresh != 49405 {
		t.Fatalf("wrong tokens %d %d %v", total, fresh, ok)
	}
	a.APIUsageRows[1].CacheReadTokens = p(138111)
	if _, _, ok = verifiedFreshTokenUsage(a); ok {
		t.Fatal("unreconciled usage treated as verified")
	}
}
