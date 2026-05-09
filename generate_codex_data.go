package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

const cacheVersion = 6
const minReadableCacheVersion = 4

// Usage mirrors the token fields emitted by Codex token_count events.
// Total is kept from the log when present; it is not recomputed from the
// other fields because Codex may define totals differently across versions.
type Usage struct {
	Input     int64 `json:"input_tokens"`
	Cached    int64 `json:"cached_input_tokens"`
	Output    int64 `json:"output_tokens"`
	Reasoning int64 `json:"reasoning_output_tokens"`
	Total     int64 `json:"total_tokens"`
}

type UsageEvent struct {
	Ts          string `json:"ts"`
	Sid         string `json:"sid"`
	Usage       Usage  `json:"usage"`
	Model       string `json:"model"`
	Snapshot    Usage  `json:"snapshot,omitempty"`
	HasSnapshot bool   `json:"hasSnapshot,omitempty"`
}

type CompletionEvent struct {
	Ts         string `json:"ts"`
	Sid        string `json:"sid"`
	Model      string `json:"model"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	TTFBMs     int64  `json:"ttfb_ms,omitempty"`
}

type FailureEvent struct {
	Ts    string `json:"ts"`
	Sid   string `json:"sid"`
	Model string `json:"model"`
}

// ParsedFile is the minimal metadata retained from one JSONL session file.
// Prompt text, assistant text, tool output, and file contents are intentionally
// not copied into this structure.
type ParsedFile struct {
	Sid              string            `json:"sid,omitempty"`
	File             string            `json:"file,omitempty"`
	Cwd              string            `json:"cwd,omitempty"`
	Model            string            `json:"model,omitempty"`
	UsageEvents      []UsageEvent      `json:"usageEvents,omitempty"`
	CompletionEvents []CompletionEvent `json:"completionEvents,omitempty"`
	FailureEvents    []FailureEvent    `json:"failureEvents,omitempty"`
	LatestLimits     map[string]any    `json:"latestLimits,omitempty"`
	LatestLimitsTs   string            `json:"latestLimitsTs,omitempty"`
	LastTotal        Usage             `json:"lastTotal,omitempty"`
	HasLastTotal     bool              `json:"hasLastTotal,omitempty"`
}

type FileCache struct {
	MtimeNs int64      `json:"mtimeNs"`
	Size    int64      `json:"size"`
	Parsed  ParsedFile `json:"parsed"`
}

type CachePayload struct {
	Version    int                  `json:"version"`
	WindowDays int                  `json:"windowDays"`
	Files      map[string]FileCache `json:"files"`
}

type SessionStats struct {
	Sid         string
	File        string
	Cwd         string
	Model       string
	StartedAt   time.Time
	EndedAt     time.Time
	DurationMs  int64
	TTFBMs      int64
	TTFBCount   int64
	Calls       int64
	Completions int64
	Failures    int64
	Usage       Usage
}

type RuntimeEvent struct {
	Ts    time.Time
	Sid   string
	Usage Usage
	Model string
}

type RuntimeTTFBEvent struct {
	Ts     time.Time
	Sid    string
	Model  string
	TTFBMs int64
}

type RuntimeFailureEvent struct {
	Ts    time.Time
	Sid   string
	Model string
}

type LoadedData struct {
	Sessions      []SessionStats
	Events        []RuntimeEvent
	Limits        map[string]any
	TTFBEvents    []RuntimeTTFBEvent
	FailureEvents []RuntimeFailureEvent
}

type CostSummary struct {
	Input          float64
	Cached         float64
	Output         float64
	Reasoning      float64
	Total          float64
	PricedTokens   int64
	UnpricedTokens int64
}

type RawExportPayload struct {
	SchemaVersion    any `json:"schemaVersion"`
	RawSchemaVersion int `json:"rawSchemaVersion"`
	Catalog          any `json:"catalog"`
	RecordBase       any `json:"recordBase"`
	RecordsV2        any `json:"recordsV2"`
	TTFBRecordsV2    any `json:"ttfbRecordsV2"`
	FailureRecordsV2 any `json:"failureRecordsV2"`
}

type bucketAccumulator struct {
	start time.Time
	end   time.Time
	usage Usage
	calls int64
	cost  CostSummary
}

type rangeBucketSet struct {
	start          time.Time
	end            time.Time
	step           time.Duration
	alignedStartMs int64
	stepMs         int64
	buckets        []bucketAccumulator
}

type peakWindowItem struct {
	ts     time.Time
	tokens int64
}

type peakWindowAccumulator struct {
	window    time.Duration
	items     []peakWindowItem
	left      int
	total     int64
	peakTotal int64
	peakTs    time.Time
	hasPeak   bool
}

type pricingRule struct {
	label    string
	patterns []string
	input    float64
	cached   float64
	output   float64
}

type PricingRuleExport struct {
	Label    string   `json:"label"`
	Patterns []string `json:"patterns"`
	Input    float64  `json:"input"`
	Cached   float64  `json:"cached"`
	Output   float64  `json:"output"`
}

type sessionFileCandidate struct {
	path    string
	mtimeNs int64
	size    int64
}

type parsedSessionFile struct {
	file   sessionFileCandidate
	parsed ParsedFile
}

func parseTime(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return ts, true
	}
	if strings.HasSuffix(value, "+00:00") {
		ts, err = time.Parse(time.RFC3339Nano, strings.TrimSuffix(value, "+00:00")+"Z")
	}
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

func isoTime(value time.Time, ok bool) string {
	if !ok || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func rateLimitPriority(limits map[string]any) int {
	if limits == nil {
		return -1
	}
	limitID := strings.ToLower(strings.TrimSpace(stringValue(limits, "limit_id")))
	limitName := strings.TrimSpace(stringValue(limits, "limit_name"))
	switch {
	case limitID == "codex":
		return 100
	case limitID != "" && limitName == "":
		return 80
	case limitID != "" || limitName != "":
		return 50
	default:
		return 10
	}
}

func preferRateLimits(candidate map[string]any, candidateTs time.Time, hasCandidateTs bool, current map[string]any, currentTs time.Time, hasCurrentTs bool) bool {
	if candidate == nil {
		return false
	}
	if current == nil {
		return true
	}
	candidatePriority := rateLimitPriority(candidate)
	currentPriority := rateLimitPriority(current)
	if candidatePriority != currentPriority {
		return candidatePriority > currentPriority
	}
	if !hasCurrentTs {
		return true
	}
	if !hasCandidateTs {
		return false
	}
	return !candidateTs.Before(currentTs)
}

func fmtInt(value int64) string {
	abs := math.Abs(float64(value))
	switch {
	case abs >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(value)/1_000_000_000)
	case abs >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(value)/1_000_000)
	case abs >= 1_000:
		return fmt.Sprintf("%.0fK", math.Round(float64(value)/1_000))
	default:
		return fmt.Sprintf("%d", value)
	}
}

func displayTime(ts time.Time, ok bool) string {
	if !ok || ts.IsZero() {
		return "--"
	}
	return ts.Local().Format("15:04")
}

func projectName(cwd, fallback string) string {
	if cwd == "" {
		return fallback
	}
	name := strings.TrimSpace(filepath.Base(cwd))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return fallback
	}
	return name
}

func number(value any) (float64, bool) {
	finite := func(v float64) bool {
		return !math.IsNaN(v) && !math.IsInf(v, 0)
	}
	switch v := value.(type) {
	case float64:
		return v, finite(v)
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil && finite(f)
	default:
		return 0, false
	}
}

func usageSnapshot(value any) (Usage, bool) {
	values, ok := value.(map[string]any)
	if !ok {
		return Usage{}, false
	}
	var usage Usage
	hasValue := false
	read := func(key string) int64 {
		raw, ok := number(values[key])
		if !ok {
			return 0
		}
		hasValue = true
		if raw < 0 {
			return 0
		}
		return int64(raw)
	}
	usage.Input = read("input_tokens")
	usage.Cached = read("cached_input_tokens")
	usage.Output = read("output_tokens")
	usage.Reasoning = read("reasoning_output_tokens")
	usage.Total = read("total_tokens")
	return usage, hasValue
}

func usageSnapshotResult(value gjson.Result) (Usage, bool) {
	if !value.Exists() || !value.IsObject() {
		return Usage{}, false
	}
	hasValue := false
	read := func(key string) int64 {
		result := value.Get(key)
		if !result.Exists() {
			return 0
		}
		hasValue = true
		raw := result.Int()
		if raw < 0 {
			return 0
		}
		return raw
	}
	usage := Usage{
		Input:     read("input_tokens"),
		Cached:    read("cached_input_tokens"),
		Output:    read("output_tokens"),
		Reasoning: read("reasoning_output_tokens"),
		Total:     read("total_tokens"),
	}
	return usage, hasValue
}

func addUsage(dst *Usage, src Usage) {
	dst.Input += src.Input
	dst.Cached += src.Cached
	dst.Output += src.Output
	dst.Reasoning += src.Reasoning
	dst.Total += src.Total
}

func usageDelta(current, previous Usage) (Usage, bool) {
	// Codex often reports cumulative total_token_usage. The dashboard needs
	// per-event usage, so subtract the previous total and clamp negative values
	// to tolerate log rewrites or counter resets.
	usage := Usage{
		Input:     max64(0, current.Input-previous.Input),
		Cached:    max64(0, current.Cached-previous.Cached),
		Output:    max64(0, current.Output-previous.Output),
		Reasoning: max64(0, current.Reasoning-previous.Reasoning),
		Total:     max64(0, current.Total-previous.Total),
	}
	return usage, usage.Input != 0 || usage.Cached != 0 || usage.Output != 0 || usage.Reasoning != 0 || usage.Total != 0
}

func usageSnapshotKey(sid, model string, usage Usage) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%d",
		sid, model, usage.Input, usage.Cached, usage.Output, usage.Reasoning, usage.Total)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func safePercent(value any) *float64 {
	raw, ok := number(value)
	if !ok {
		return nil
	}
	if raw < 0 {
		raw = 0
	}
	if raw > 100 {
		raw = 100
	}
	return &raw
}

func clampPercentValue(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func successFailureRates(calls int64, failures int) (float64, float64) {
	if calls <= 0 {
		if failures > 0 {
			return 0, 100
		}
		return 100, 0
	}
	failureRate := clampPercentValue(float64(failures) / float64(calls) * 100)
	return 100 - failureRate, failureRate
}

func cutoffForDays(days int, now time.Time) time.Time {
	if days <= 0 {
		return time.Time{}
	}
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}

func localDayStart(value time.Time) time.Time {
	local := value.Local()
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
}

func ymd(value time.Time) string {
	return value.Local().Format("2006-01-02")
}

func formatRangeLabel(start, end time.Time, preset string) string {
	switch preset {
	case "24h":
		return "最近24小时"
	case "today":
		return "今天"
	case "7":
		return "7天内"
	case "30":
		return "30天内"
	case "history":
		return "历史总览"
	default:
		return fmt.Sprintf("%s 至 %s", ymd(start), ymd(end.Add(-time.Millisecond)))
	}
}

func cacheCoversDays(cachedDays int, requestedDays int) bool {
	if requestedDays <= 0 {
		return cachedDays <= 0
	}
	if cachedDays <= 0 {
		return true
	}
	return cachedDays >= requestedDays
}

func loadCache(cachePath string, days int) map[string]FileCache {
	// A cache generated for a shorter window cannot safely answer a longer one
	// because older files may have been skipped. Older readable cache versions
	// can still serve unchanged files; they just miss newer append-only metadata.
	if cachePath == "" {
		return map[string]FileCache{}
	}
	body, err := os.ReadFile(cachePath)
	if err != nil {
		return map[string]FileCache{}
	}
	var payload CachePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return map[string]FileCache{}
	}
	if payload.Version < minReadableCacheVersion || payload.Version > cacheVersion || !cacheCoversDays(payload.WindowDays, days) || payload.Files == nil {
		return map[string]FileCache{}
	}
	return payload.Files
}

func writeCache(cachePath string, days int, files map[string]FileCache) {
	// Write through a temporary file so an interrupted run does not leave a
	// partially-written cache that would poison later hot starts.
	if cachePath == "" {
		return
	}
	dir := filepath.Dir(cachePath)
	if dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	payload := CachePayload{Version: cacheVersion, WindowDays: days, Files: files}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(cachePath)+".*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Rename(tmpName, cachePath); err != nil {
		_ = os.Remove(cachePath)
		_ = os.Rename(tmpName, cachePath)
	}
}

func cacheStampPath(cachePath string) string {
	if cachePath == "" {
		return ""
	}
	return cachePath + ".stamp"
}

func sourceNewerThan(ts time.Time) bool {
	for _, path := range []string{"generate_codex_data.go", "go.mod", "go.sum"} {
		info, err := os.Stat(path)
		if err == nil && info.ModTime().After(ts) {
			return true
		}
	}
	return false
}

func fileSignature(root string, out string, rawOut string, days int, trendMinutes int, files []sessionFileCandidate) string {
	var totalSize int64
	var maxMtimeNs int64
	for _, file := range files {
		totalSize += file.size
		if file.mtimeNs > maxMtimeNs {
			maxMtimeNs = file.mtimeNs
		}
	}
	return fmt.Sprintf("v=%d\nroot=%s\nout=%s\nraw=%s\ndays=%d\ntrend=%d\nfiles=%d:%d:%d\n",
		cacheVersion, root, out, rawOut, days, trendMinutes, len(files), totalSize, maxMtimeNs)
}

func outputIsStampedFresh(outPath string, rawOutPath string, files []sessionFileCandidate, stampPath string, expectedSignature string) bool {
	if outPath == "" || stampPath == "" {
		return false
	}
	outInfo, err := os.Stat(outPath)
	if err != nil || outInfo.IsDir() || sourceNewerThan(outInfo.ModTime()) {
		return false
	}
	rawInfo, err := os.Stat(rawOutPath)
	if rawOutPath == "" || err != nil || rawInfo.IsDir() || sourceNewerThan(rawInfo.ModTime()) {
		return false
	}
	stampInfo, err := os.Stat(stampPath)
	if err != nil || stampInfo.IsDir() || stampInfo.ModTime().Before(outInfo.ModTime()) || stampInfo.ModTime().Before(rawInfo.ModTime()) {
		return false
	}
	body, err := os.ReadFile(stampPath)
	if err != nil {
		return false
	}
	if string(body) != expectedSignature || !outputHasCurrentSchemaForRawPath(outPath, browserRawDataPath(outPath, rawOutPath)) || !rawOutputHasCurrentSchema(rawOutPath) {
		return false
	}
	outMtime := outInfo.ModTime()
	if rawInfo.ModTime().Before(outMtime) {
		outMtime = rawInfo.ModTime()
	}
	for _, file := range files {
		if time.Unix(0, file.mtimeNs).After(outMtime) {
			return false
		}
	}
	return true
}

func outputHasCurrentSchema(outPath string) bool {
	return outputHasCurrentSchemaForRawPath(outPath, "")
}

func outputHasCurrentSchemaForRawPath(outPath string, rawDataPath string) bool {
	needles := []string{`"schemaVersion":2`, `"rawDataPath"`, `"views"`, `"pricingRules"`}
	if rawDataPath != "" {
		quoted, err := json.Marshal(rawDataPath)
		if err != nil {
			return false
		}
		needles = append(needles, `"rawDataPath":`+string(quoted))
	}
	return fileHeadContainsAll(outPath, 128*1024, needles...)
}

func rawOutputHasCurrentSchema(outPath string) bool {
	return fileHeadContainsAll(outPath, 16*1024, `window.CODEXSCOPE_RAW_DATA`, `"schemaVersion":2`, `"rawSchemaVersion":1`)
}

func fileHeadContainsAll(path string, limit int64, needles ...string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	if limit <= 0 {
		limit = 4096
	}
	body, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return false
	}
	text := string(body)
	for _, needle := range needles {
		if !strings.Contains(text, needle) {
			return false
		}
	}
	return true
}

func writeRunStamp(stampPath string, signature string) {
	if stampPath == "" {
		return
	}
	_ = os.WriteFile(stampPath, []byte(signature), 0o644)
}

func canAppendFrom(path string, offset int64) bool {
	if offset <= 0 {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	if _, err := file.Seek(offset-1, io.SeekStart); err != nil {
		return false
	}
	var lastByte [1]byte
	n, err := file.Read(lastByte[:])
	return err == nil && n == 1 && lastByte[0] == '\n'
}

func parseSessionFile(path string, cutoff time.Time) ParsedFile {
	parsed := ParsedFile{Sid: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), File: path, Model: "unknown"}
	return parseSessionFileFrom(path, cutoff, parsed, 0)
}

func parseSessionFileAppend(path string, cutoff time.Time, cached ParsedFile, offset int64) ParsedFile {
	if cached.Sid == "" {
		cached.Sid = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if cached.File == "" {
		cached.File = path
	}
	if cached.Model == "" {
		cached.Model = "unknown"
	}
	return parseSessionFileFrom(path, cutoff, cached, offset)
}

func parseSessionFileFrom(path string, cutoff time.Time, parsed ParsedFile, offset int64) ParsedFile {
	// Parse JSONL incrementally instead of loading the whole file. Large Codex
	// sessions can grow quickly, and only a small set of metadata fields is
	// needed for the dashboard.
	file, err := os.Open(path)
	if err != nil {
		return parsed
	}
	defer file.Close()
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return parsed
		}
	}

	reader := bufio.NewReaderSize(file, 1024*1024)
	prevTotal := parsed.LastTotal
	hasPrevTotal := parsed.HasLastTotal
	var latestLimitsTs time.Time
	hasLatestLimitsTs := false
	if ts, ok := parseTime(parsed.LatestLimitsTs); ok {
		latestLimitsTs = ts
		hasLatestLimitsTs = true
	}

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			obj := gjson.ParseBytes(line)
			topType := obj.Get("type").String()
			payloadType := obj.Get("payload.type").String()

			switch {
			case topType == "session_meta":
				if id := obj.Get("payload.id").String(); id != "" {
					parsed.Sid = id
				}
				if cwd := obj.Get("payload.cwd").String(); cwd != "" {
					parsed.Cwd = cwd
				}
			case topType == "turn_context":
				if model := obj.Get("payload.model").String(); model != "" {
					parsed.Model = model
				}
				if cwd := obj.Get("payload.cwd").String(); cwd != "" {
					parsed.Cwd = cwd
				}
			case payloadType == "token_count":
				ts, hasTs := parseTime(obj.Get("timestamp").String())
				limitsResult := obj.Get("payload.rate_limits")
				if limitsResult.Exists() && limitsResult.IsObject() {
					var limits map[string]any
					if json.Unmarshal([]byte(limitsResult.Raw), &limits) == nil {
						// Codex can emit both global quota and model-specific quota
						// records. Prefer the global codex object; for the same
						// quota class, keep the newest record.
						if preferRateLimits(limits, ts, hasTs, parsed.LatestLimits, latestLimitsTs, hasLatestLimitsTs) {
							parsed.LatestLimits = limits
							if hasTs {
								latestLimitsTs = ts
								hasLatestLimitsTs = true
								parsed.LatestLimitsTs = isoTime(ts, true)
							}
						}
					}
				}
				lastUsage, hasLast := usageSnapshotResult(obj.Get("payload.info.last_token_usage"))
				totalUsage, hasTotal := usageSnapshotResult(obj.Get("payload.info.total_token_usage"))
				prev := prevTotal
				hadPrev := hasPrevTotal
				if hasTotal {
					prevTotal = totalUsage
					hasPrevTotal = true
					parsed.LastTotal = totalUsage
					parsed.HasLastTotal = true
				}
				if hasTs && !ts.Before(cutoff) {
					var usage Usage
					hasUsage := false
					// Prefer cumulative deltas when possible. Fall back to
					// last_token_usage for the first event or older log shapes.
					if hasTotal && hadPrev {
						usage, hasUsage = usageDelta(totalUsage, prev)
					} else if hasLast {
						usage, hasUsage = lastUsage, true
					}
					if hasUsage {
						event := UsageEvent{Ts: isoTime(ts, true), Sid: parsed.Sid, Usage: usage, Model: parsed.Model}
						if hasTotal {
							event.Snapshot = totalUsage
							event.HasSnapshot = true
						}
						parsed.UsageEvents = append(parsed.UsageEvents, event)
					}
				}
			case payloadType == "task_complete":
				ts, hasTs := parseTime(obj.Get("timestamp").String())
				if hasTs && !ts.Before(cutoff) {
					event := CompletionEvent{Ts: isoTime(ts, true), Sid: parsed.Sid, Model: parsed.Model}
					if duration := obj.Get("payload.duration_ms"); duration.Exists() {
						event.DurationMs = duration.Int()
					}
					if ttfb := obj.Get("payload.time_to_first_token_ms"); ttfb.Exists() {
						event.TTFBMs = ttfb.Int()
					}
					parsed.CompletionEvents = append(parsed.CompletionEvents, event)
				}
			case payloadType == "error" || payloadType == "turn_aborted":
				ts, hasTs := parseTime(obj.Get("timestamp").String())
				if hasTs && !ts.Before(cutoff) {
					parsed.FailureEvents = append(parsed.FailureEvents, FailureEvent{Ts: isoTime(ts, true), Sid: parsed.Sid, Model: parsed.Model})
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}
	}
	return parsed
}

func markSeen(stat *SessionStats, ts time.Time) {
	if stat.StartedAt.IsZero() || ts.Before(stat.StartedAt) {
		stat.StartedAt = ts
	}
	if stat.EndedAt.IsZero() || ts.After(stat.EndedAt) {
		stat.EndedAt = ts
	}
}

func mergeSessionFile(parsed ParsedFile, cutoff time.Time, loaded *LoadedData, latestLimitsTs *time.Time, seenUsageEvents map[string]struct{}) {
	// Parsed files are per-log artifacts; LoadedData is the normalized runtime
	// model used for totals, rankings, chart records, and latest quota status.
	sid := parsed.Sid
	if sid == "" {
		sid = strings.TrimSuffix(filepath.Base(parsed.File), filepath.Ext(parsed.File))
	}
	model := parsed.Model
	if model == "" {
		model = "unknown"
	}
	stat := SessionStats{Sid: sid, File: parsed.File, Cwd: parsed.Cwd, Model: model}

	for _, event := range parsed.UsageEvents {
		ts, ok := parseTime(event.Ts)
		if !ok || ts.Before(cutoff) {
			continue
		}
		eventSid := event.Sid
		if eventSid == "" {
			eventSid = sid
		}
		eventModel := event.Model
		if eventModel == "" {
			eventModel = model
		}
		if event.HasSnapshot {
			key := usageSnapshotKey(eventSid, eventModel, event.Snapshot)
			if _, ok := seenUsageEvents[key]; ok {
				continue
			}
			seenUsageEvents[key] = struct{}{}
		}
		markSeen(&stat, ts)
		addUsage(&stat.Usage, event.Usage)
		stat.Calls++
		loaded.Events = append(loaded.Events, RuntimeEvent{Ts: ts, Sid: eventSid, Usage: event.Usage, Model: eventModel})
	}

	for _, event := range parsed.CompletionEvents {
		ts, ok := parseTime(event.Ts)
		if !ok || ts.Before(cutoff) {
			continue
		}
		markSeen(&stat, ts)
		stat.Completions++
		stat.DurationMs += event.DurationMs
		if event.TTFBMs > 0 {
			stat.TTFBMs += event.TTFBMs
			stat.TTFBCount++
			eventSid := event.Sid
			if eventSid == "" {
				eventSid = sid
			}
			eventModel := event.Model
			if eventModel == "" {
				eventModel = model
			}
			loaded.TTFBEvents = append(loaded.TTFBEvents, RuntimeTTFBEvent{Ts: ts, Sid: eventSid, Model: eventModel, TTFBMs: event.TTFBMs})
		}
	}

	for _, event := range parsed.FailureEvents {
		ts, ok := parseTime(event.Ts)
		if !ok || ts.Before(cutoff) {
			continue
		}
		markSeen(&stat, ts)
		stat.Failures++
		eventSid := event.Sid
		if eventSid == "" {
			eventSid = sid
		}
		eventModel := event.Model
		if eventModel == "" {
			eventModel = model
		}
		loaded.FailureEvents = append(loaded.FailureEvents, RuntimeFailureEvent{Ts: ts, Sid: eventSid, Model: eventModel})
	}

	if parsed.LatestLimits != nil {
		ts, ok := parseTime(parsed.LatestLimitsTs)
		if preferRateLimits(parsed.LatestLimits, ts, ok, loaded.Limits, *latestLimitsTs, !latestLimitsTs.IsZero()) {
			loaded.Limits = parsed.LatestLimits
			if ok {
				*latestLimitsTs = ts
			}
		}
	}

	if stat.Calls != 0 || stat.Completions != 0 || stat.Failures != 0 {
		loaded.Sessions = append(loaded.Sessions, stat)
	}
}

func collectSessionFiles(root string, cutoff time.Time) []sessionFileCandidate {
	files := make([]sessionFileCandidate, 0, 1024)
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry == nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		// Use one day of slack around the event cutoff because file mtimes and
		// event timestamps can diverge after syncs, copies, or manual moves.
		if info.ModTime().UTC().Before(cutoff.Add(-24 * time.Hour)) {
			return nil
		}
		files = append(files, sessionFileCandidate{
			path:    path,
			mtimeNs: info.ModTime().UnixNano(),
			size:    info.Size(),
		})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files
}

func parseSessionFiles(files []sessionFileCandidate, cutoff time.Time, cacheFiles map[string]FileCache) []parsedSessionFile {
	// Bound concurrency to keep launches fast without making the generator noisy
	// on small laptops. Cached files skip JSON parsing when mtime and size match.
	results := make([]parsedSessionFile, len(files))
	if len(files) == 0 {
		return results
	}

	workerCount := runtime.GOMAXPROCS(0) * 2
	if workerCount < 2 {
		workerCount = 2
	}
	if workerCount > 16 {
		workerCount = 16
	}
	if workerCount > len(files) {
		workerCount = len(files)
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer wg.Done()
			for index := range jobs {
				file := files[index]
				cached, ok := cacheFiles[file.path]
				var parsed ParsedFile
				if ok && cached.MtimeNs == file.mtimeNs && cached.Size == file.size {
					parsed = cached.Parsed
				} else if ok && file.size > cached.Size && cached.Parsed.HasLastTotal && canAppendFrom(file.path, cached.Size) {
					parsed = parseSessionFileAppend(file.path, cutoff, cached.Parsed, cached.Size)
				} else {
					parsed = parseSessionFile(file.path, cutoff)
				}
				results[index] = parsedSessionFile{file: file, parsed: parsed}
			}
		}()
	}
	for index := range files {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	return results
}

func loadSessions(root string, cutoff time.Time, cachePath string, days int, cacheFiles map[string]FileCache) LoadedData {
	loaded := LoadedData{}
	if cacheFiles == nil {
		cacheFiles = loadCache(cachePath, days)
	}
	var latestLimitsTs time.Time

	files := collectSessionFiles(root, cutoff)
	parsedFiles := parseSessionFiles(files, cutoff, cacheFiles)
	nextCacheFiles := make(map[string]FileCache, len(parsedFiles))
	usageEventCount := 0
	ttfbEventCount := 0
	failureEventCount := 0
	for _, result := range parsedFiles {
		usageEventCount += len(result.parsed.UsageEvents)
		ttfbEventCount += len(result.parsed.CompletionEvents)
		failureEventCount += len(result.parsed.FailureEvents)
	}
	loaded.Sessions = make([]SessionStats, 0, len(parsedFiles))
	loaded.Events = make([]RuntimeEvent, 0, usageEventCount)
	loaded.TTFBEvents = make([]RuntimeTTFBEvent, 0, ttfbEventCount)
	loaded.FailureEvents = make([]RuntimeFailureEvent, 0, failureEventCount)
	seenUsageEvents := make(map[string]struct{}, usageEventCount)
	for _, result := range parsedFiles {
		nextCacheFiles[result.file.path] = FileCache{MtimeNs: result.file.mtimeNs, Size: result.file.size, Parsed: result.parsed}
		mergeSessionFile(result.parsed, cutoff, &loaded, &latestLimitsTs, seenUsageEvents)
	}

	writeCache(cachePath, days, nextCacheFiles)
	return loaded
}

func outputIsFresh(outPath string, rawOutPath string, files []sessionFileCandidate, cacheFiles map[string]FileCache) bool {
	if outPath == "" || rawOutPath == "" || len(files) == 0 || len(cacheFiles) != len(files) {
		return false
	}
	outInfo, err := os.Stat(outPath)
	if err != nil || outInfo.IsDir() {
		return false
	}
	expectedRawDataPath := browserRawDataPath(outPath, rawOutPath)
	if !outputHasCurrentSchemaForRawPath(outPath, expectedRawDataPath) {
		return false
	}
	rawInfo, err := os.Stat(rawOutPath)
	if err != nil || rawInfo.IsDir() || !rawOutputHasCurrentSchema(rawOutPath) {
		return false
	}
	if sourceInfo, err := os.Stat("generate_codex_data.go"); err == nil && outInfo.ModTime().Before(sourceInfo.ModTime()) {
		return false
	}
	if sourceInfo, err := os.Stat("generate_codex_data.go"); err == nil && rawInfo.ModTime().Before(sourceInfo.ModTime()) {
		return false
	}
	for _, file := range files {
		cached, ok := cacheFiles[file.path]
		if !ok || cached.MtimeNs != file.mtimeNs || cached.Size != file.size {
			return false
		}
	}
	return true
}

var modelPricingUSDPerM = []pricingRule{
	{label: "gpt-5.5", patterns: []string{"gpt-5.5"}, input: 5.00, cached: 0.50, output: 30.00},
	{label: "gpt-5.4 mini", patterns: []string{"gpt-5.4-mini", "gpt_5.4_mini", "gpt 5.4 mini"}, input: 0.75, cached: 0.075, output: 4.50},
	{label: "gpt-5.4", patterns: []string{"gpt-5.4"}, input: 2.50, cached: 0.25, output: 15.00},
	{label: "gpt-5.3 codex spark", patterns: []string{"gpt-5.3-codex-spark", "gpt_5.3_codex_spark", "gpt 5.3 codex spark"}, input: 1.75, cached: 0.175, output: 14.00},
	{label: "gpt-5.3 codex", patterns: []string{"gpt-5.3-codex", "gpt_5.3_codex", "gpt 5.3 codex"}, input: 1.75, cached: 0.175, output: 14.00},
	{label: "gpt-5.2 codex", patterns: []string{"gpt-5.2-codex", "gpt_5.2_codex", "gpt 5.2 codex"}, input: 1.75, cached: 0.175, output: 14.00},
	{label: "gpt-5 / 5.1 codex", patterns: []string{"gpt-5.1-codex", "gpt_5.1_codex", "gpt 5.1 codex", "gpt-5-codex", "gpt_5_codex", "gpt 5 codex", "gpt-5"}, input: 1.25, cached: 0.125, output: 10.00},
}

func pricingRulesPayload() []PricingRuleExport {
	out := make([]PricingRuleExport, 0, len(modelPricingUSDPerM))
	for _, rule := range modelPricingUSDPerM {
		out = append(out, PricingRuleExport{
			Label:    rule.label,
			Patterns: append([]string(nil), rule.patterns...),
			Input:    rule.input,
			Cached:   rule.cached,
			Output:   rule.output,
		})
	}
	return out
}

func pricingForModel(model string) *pricingRule {
	model = strings.ToLower(model)
	for i := range modelPricingUSDPerM {
		for _, pattern := range modelPricingUSDPerM[i].patterns {
			if strings.Contains(model, pattern) {
				return &modelPricingUSDPerM[i]
			}
		}
	}
	return nil
}

func addCost(dst *CostSummary, src CostSummary) {
	dst.Input += src.Input
	dst.Cached += src.Cached
	dst.Output += src.Output
	dst.Reasoning += src.Reasoning
	dst.Total += src.Total
	dst.PricedTokens += src.PricedTokens
	dst.UnpricedTokens += src.UnpricedTokens
}

func priceUsage(model string, usage Usage) CostSummary {
	inputTokens := max64(0, usage.Input)
	cachedRaw := max64(0, usage.Cached)
	outputTokens := max64(0, usage.Output)
	reasoningRaw := max64(0, usage.Reasoning)
	cachedTokens := cachedRaw
	if inputTokens > 0 && cachedTokens > inputTokens {
		cachedTokens = inputTokens
	}
	billableInput := max64(0, inputTokens-cachedTokens)
	billedReasoning := reasoningRaw
	if outputTokens > 0 && billedReasoning > outputTokens {
		billedReasoning = outputTokens
	}
	visibleOutput := max64(0, outputTokens-billedReasoning)
	pricedTokens := billableInput + cachedTokens + visibleOutput + billedReasoning
	rule := pricingForModel(model)
	if rule == nil {
		return CostSummary{UnpricedTokens: pricedTokens}
	}
	multiplier := 1.0 / 1_000_000.0
	input := float64(billableInput) * rule.input * multiplier
	cached := float64(cachedTokens) * rule.cached * multiplier
	output := float64(visibleOutput) * rule.output * multiplier
	reasoning := float64(billedReasoning) * rule.output * multiplier
	return CostSummary{
		Input:        input,
		Cached:       cached,
		Output:       output,
		Reasoning:    reasoning,
		Total:        input + cached + output + reasoning,
		PricedTokens: pricedTokens,
	}
}

func chooseNiceStep(duration time.Duration, targetPoints int) time.Duration {
	if targetPoints <= 0 {
		targetPoints = 180
	}
	ideal := duration / time.Duration(targetPoints)
	steps := []time.Duration{
		time.Minute,
		2 * time.Minute,
		5 * time.Minute,
		10 * time.Minute,
		15 * time.Minute,
		30 * time.Minute,
		time.Hour,
		2 * time.Hour,
		3 * time.Hour,
		6 * time.Hour,
		12 * time.Hour,
		24 * time.Hour,
		48 * time.Hour,
		72 * time.Hour,
		7 * 24 * time.Hour,
		14 * 24 * time.Hour,
		30 * 24 * time.Hour,
	}
	for _, step := range steps {
		if step >= ideal {
			return step
		}
	}
	return steps[len(steps)-1]
}

func formatBucketLabel(ts time.Time, step time.Duration, start, end time.Time) string {
	local := ts.Local()
	timeLabel := local.Format("15:04")
	if step >= 24*time.Hour {
		return fmt.Sprintf("%d/%d", int(local.Month()), local.Day())
	}
	if end.Sub(start) > 24*time.Hour {
		return fmt.Sprintf("%d/%d %s", int(local.Month()), local.Day(), timeLabel)
	}
	return timeLabel
}

func formatPeakLabelForRange(ts time.Time, ok bool, start, end time.Time) string {
	if !ok || ts.IsZero() {
		return "--"
	}
	local := ts.Local()
	if end.Sub(start) > 24*time.Hour {
		return fmt.Sprintf("%d/%d %s", int(local.Month()), local.Day(), local.Format("15:04"))
	}
	return local.Format("15:04")
}

func formatStepLabel(step time.Duration) string {
	minutes := int(math.Round(step.Minutes()))
	if minutes < 60 {
		return fmt.Sprintf("%d分钟/点", minutes)
	}
	if minutes < 24*60 {
		return fmt.Sprintf("%d小时/点", minutes/60)
	}
	return fmt.Sprintf("%d天/点", minutes/(24*60))
}

func newRangeBucketSet(start, end time.Time, step time.Duration) *rangeBucketSet {
	if !end.After(start) {
		end = start.Add(time.Minute)
	}
	if step < time.Minute {
		step = time.Minute
	}
	startMs := start.UnixMilli()
	stepMs := int64(step / time.Millisecond)
	alignedStartMs := (startMs / stepMs) * stepMs
	endMs := end.UnixMilli()
	alignedEndMs := ((endMs + stepMs - 1) / stepMs) * stepMs
	bucketCount := int(math.Max(1, math.Ceil(float64(alignedEndMs-alignedStartMs)/float64(stepMs))))
	buckets := make([]bucketAccumulator, bucketCount)
	for i := range buckets {
		buckets[i].start = time.UnixMilli(alignedStartMs + int64(i)*stepMs)
		buckets[i].end = time.UnixMilli(alignedStartMs + int64(i+1)*stepMs)
		if i == bucketCount-1 {
			buckets[i].end = time.UnixMilli(alignedEndMs + 1)
		}
	}
	return &rangeBucketSet{
		start:          start,
		end:            end,
		step:           step,
		alignedStartMs: alignedStartMs,
		stepMs:         stepMs,
		buckets:        buckets,
	}
}

func (set *rangeBucketSet) addEvent(event RuntimeEvent, cost CostSummary) {
	if set == nil || event.Ts.Before(set.start) || event.Ts.After(set.end) {
		return
	}
	idx := int((event.Ts.UnixMilli() - set.alignedStartMs) / set.stepMs)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(set.buckets) {
		idx = len(set.buckets) - 1
	}
	addUsage(&set.buckets[idx].usage, event.Usage)
	set.buckets[idx].calls++
	addCost(&set.buckets[idx].cost, cost)
}

func (set *rangeBucketSet) compactTrend() [][]any {
	if set == nil {
		return nil
	}
	out := make([][]any, 0, len(set.buckets))
	for _, bucket := range set.buckets {
		out = append(out, []any{
			formatBucketLabel(bucket.start, set.step, set.start, set.end),
			bucket.usage.Total,
			bucket.usage.Cached,
			bucket.usage.Output,
			bucket.usage.Input,
			bucket.usage.Reasoning,
			bucket.calls,
			roundCost(bucket.cost.Total),
		})
	}
	return out
}

func (set *rangeBucketSet) compactDistribution() [][]any {
	if set == nil {
		return nil
	}
	out := make([][]any, 0, len(set.buckets))
	for _, bucket := range set.buckets {
		out = append(out, []any{
			formatBucketLabel(bucket.start, set.step, set.start, set.end),
			bucket.usage.Total,
			bucket.calls,
			roundCost(bucket.cost.Total),
		})
	}
	return out
}

func (set *rangeBucketSet) stepLabel() string {
	if set == nil || len(set.buckets) == 0 {
		return ""
	}
	return formatStepLabel(set.step)
}

func (set *rangeBucketSet) stepMinutes() float64 {
	if set == nil || len(set.buckets) == 0 {
		return 0
	}
	return set.step.Minutes()
}

func roundCost(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}

func (acc *peakWindowAccumulator) add(ts time.Time, tokens int64) {
	if acc.window <= 0 {
		acc.window = time.Minute
	}
	acc.items = append(acc.items, peakWindowItem{ts: ts, tokens: tokens})
	acc.total += tokens
	for acc.left < len(acc.items) && ts.Sub(acc.items[acc.left].ts) >= acc.window {
		acc.total -= acc.items[acc.left].tokens
		acc.left++
	}
	if acc.left > 1024 && acc.left*2 > len(acc.items) {
		copy(acc.items, acc.items[acc.left:])
		acc.items = acc.items[:len(acc.items)-acc.left]
		acc.left = 0
	}
	if acc.total > acc.peakTotal {
		acc.peakTotal = acc.total
		acc.peakTs = ts
		acc.hasPeak = true
	}
}

func (acc *peakWindowAccumulator) result() (int64, time.Time, bool) {
	return acc.peakTotal, acc.peakTs, acc.hasPeak
}

func buildCostParts(costs CostSummary) []map[string]any {
	parts := []struct {
		key       string
		name      string
		value     float64
		className string
	}{
		{"input", "输入", costs.Input, "cost-input"},
		{"cached", "缓存", costs.Cached, "cost-cache"},
		{"output", "输出", costs.Output, "cost-output"},
		{"reasoning", "推理", costs.Reasoning, "cost-reasoning"},
	}
	rows := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		percent := 0.0
		if costs.Total > 0 {
			percent = part.value / costs.Total * 100
		}
		rows = append(rows, map[string]any{
			"key":       part.key,
			"name":      part.name,
			"value":     part.value,
			"className": part.className,
			"percent":   percent,
		})
	}
	return rows
}

func runtimeItemsInRange[T any](items []T, start, end time.Time, itemTime func(T) time.Time) []T {
	if len(items) == 0 {
		return nil
	}
	lo := sort.Search(len(items), func(i int) bool {
		return !itemTime(items[i]).Before(start)
	})
	hi := sort.Search(len(items), func(i int) bool {
		return itemTime(items[i]).After(end)
	})
	if lo > hi {
		return nil
	}
	return items[lo:hi]
}

func runtimeEventsInRange(events []RuntimeEvent, start, end time.Time) []RuntimeEvent {
	return runtimeItemsInRange(events, start, end, func(event RuntimeEvent) time.Time {
		return event.Ts
	})
}

func runtimeFailuresInRange(events []RuntimeFailureEvent, start, end time.Time) []RuntimeFailureEvent {
	return runtimeItemsInRange(events, start, end, func(event RuntimeFailureEvent) time.Time {
		return event.Ts
	})
}

func runtimeTTFBInRange(events []RuntimeTTFBEvent, start, end time.Time) []RuntimeTTFBEvent {
	return runtimeItemsInRange(events, start, end, func(event RuntimeTTFBEvent) time.Time {
		return event.Ts
	})
}

func loadedDataBounds(loaded LoadedData, fallback time.Time) (time.Time, time.Time, bool) {
	start := fallback
	end := fallback
	hasData := false
	visit := func(ts time.Time) {
		if ts.IsZero() {
			return
		}
		if !hasData || ts.Before(start) {
			start = ts
		}
		if !hasData || ts.After(end) {
			end = ts
		}
		hasData = true
	}
	for _, event := range loaded.Events {
		visit(event.Ts)
	}
	for _, event := range loaded.TTFBEvents {
		visit(event.Ts)
	}
	for _, event := range loaded.FailureEvents {
		visit(event.Ts)
	}
	return start, end, hasData
}

func buildView(key, label string, start, end time.Time, loaded LoadedData, sessionCatalog map[string]map[string]string, limits map[string]any) map[string]any {
	type sessionRow struct {
		name     string
		model    string
		tokens   int64
		requests int64
		status   string
	}
	type modelRow struct {
		name         string
		usage        Usage
		tokens       int64
		requests     int64
		cost         float64
		latencyTotal int64
		latencyCount int64
	}

	var totals Usage
	var calls int64
	var costs CostSummary
	bySession := map[string]*sessionRow{}
	byModel := map[string]*modelRow{}

	duration := end.Sub(start)
	if duration <= 0 {
		duration = time.Minute
	}
	trendBuckets := newRangeBucketSet(start, end, chooseNiceStep(duration, 180))
	distributionBuckets := newRangeBucketSet(start, end, chooseNiceStep(duration, 32))
	peak := peakWindowAccumulator{window: time.Minute}

	for _, event := range runtimeEventsInRange(loaded.Events, start, end) {
		addUsage(&totals, event.Usage)
		calls++
		model := nonEmpty(event.Model, "unknown")
		cost := priceUsage(model, event.Usage)
		addCost(&costs, cost)
		trendBuckets.addEvent(event, cost)
		distributionBuckets.addEvent(event, cost)
		peak.add(event.Ts, event.Usage.Total)

		catalog := sessionCatalog[event.Sid]
		name := fmt.Sprintf("会话 %s", tail(event.Sid, 6))
		if catalog != nil && catalog["name"] != "" {
			name = catalog["name"]
		}
		session := bySession[event.Sid]
		if session == nil {
			session = &sessionRow{name: name, model: model, status: "ok"}
			bySession[event.Sid] = session
		}
		session.tokens += event.Usage.Total
		session.requests++

		row := byModel[model]
		if row == nil {
			row = &modelRow{name: model}
			byModel[model] = row
		}
		addUsage(&row.usage, event.Usage)
		row.tokens += event.Usage.Total
		row.requests++
		row.cost += cost.Total
	}

	failureCount := 0
	for _, event := range runtimeFailuresInRange(loaded.FailureEvents, start, end) {
		failureCount++
		if session := bySession[event.Sid]; session != nil {
			session.status = "warn"
		}
	}
	for _, event := range runtimeTTFBInRange(loaded.TTFBEvents, start, end) {
		model := nonEmpty(event.Model, "unknown")
		row := byModel[model]
		if row == nil {
			row = &modelRow{name: model}
			byModel[model] = row
		}
		row.latencyTotal += event.TTFBMs
		row.latencyCount++
	}

	peakTotal, peakTs, hasPeak := peak.result()

	cacheHit := 0.0
	if totals.Input > 0 {
		cacheHit = float64(totals.Cached) / float64(totals.Input) * 100
	}
	successRate, failureRate := successFailureRates(calls, failureCount)

	sessionRows := make([]*sessionRow, 0, len(bySession))
	for _, row := range bySession {
		sessionRows = append(sessionRows, row)
	}
	sort.Slice(sessionRows, func(i, j int) bool { return sessionRows[i].tokens > sessionRows[j].tokens })
	maxSessionTokens := int64(1)
	maxSessionRequests := int64(1)
	for _, row := range sessionRows {
		if row.tokens > maxSessionTokens {
			maxSessionTokens = row.tokens
		}
		if row.requests > maxSessionRequests {
			maxSessionRequests = row.requests
		}
	}
	if len(sessionRows) > 20 {
		sessionRows = sessionRows[:20]
	}
	sessionOut := make([]map[string]any, 0, len(sessionRows))
	for index, row := range sessionRows {
		sessionOut = append(sessionOut, map[string]any{
			"rank":           index + 1,
			"name":           row.name,
			"model":          row.model,
			"tokens":         row.tokens,
			"tokensLabel":    fmtInt(row.tokens),
			"requests":       row.requests,
			"tokenPercent":   int(math.Round(float64(row.tokens) / float64(maxSessionTokens) * 100)),
			"requestPercent": int(math.Round(float64(row.requests) / float64(maxSessionRequests) * 100)),
			"status":         row.status,
		})
	}

	modelRows := make([]*modelRow, 0, len(byModel))
	for _, row := range byModel {
		modelRows = append(modelRows, row)
	}
	sort.Slice(modelRows, func(i, j int) bool { return modelRows[i].tokens > modelRows[j].tokens })
	maxModelTokens := int64(1)
	for _, row := range modelRows {
		if row.tokens > maxModelTokens {
			maxModelTokens = row.tokens
		}
	}
	modelLimit := min(len(modelRows), 12)
	modelOut := make([]map[string]any, 0, modelLimit)
	for index := 0; index < modelLimit; index++ {
		row := modelRows[index]
		latency := 0.0
		latencyLabel := "--"
		if row.latencyCount > 0 {
			latency = float64(row.latencyTotal) / float64(row.latencyCount) / 1000
			latencyLabel = fmt.Sprintf("%.2fs", latency)
		}
		modelOut = append(modelOut, map[string]any{
			"name":         row.name,
			"tokens":       row.tokens,
			"tokensLabel":  fmtInt(row.tokens),
			"requests":     row.requests,
			"input":        row.usage.Input,
			"cached":       row.usage.Cached,
			"output":       row.usage.Output,
			"reasoning":    row.usage.Reasoning,
			"latency":      latency,
			"latencyLabel": latencyLabel,
			"cost":         row.cost,
			"percent":      int(math.Round(float64(row.tokens) / float64(maxModelTokens) * 100)),
		})
	}

	sort.Slice(modelRows, func(i, j int) bool { return modelRows[i].cost > modelRows[j].cost })
	costLimit := min(len(modelRows), 4)
	maxModelCost := 1.0
	for i := 0; i < costLimit; i++ {
		if modelRows[i].cost > maxModelCost {
			maxModelCost = modelRows[i].cost
		}
	}
	costModels := make([]map[string]any, 0, costLimit)
	for i := 0; i < costLimit; i++ {
		row := modelRows[i]
		costModels = append(costModels, map[string]any{
			"name":    row.name,
			"rank":    i + 1,
			"cost":    row.cost,
			"percent": int(math.Round(row.cost / maxModelCost * 100)),
		})
	}

	primary := mapValue(limits, "primary")
	secondary := mapValue(limits, "secondary")
	primaryUsed := safePercent(primary["used_percent"])
	secondaryUsed := safePercent(secondary["used_percent"])
	primaryReset, hasPrimaryReset := resetTime(primary["resets_at"])
	secondaryReset, hasSecondaryReset := resetTime(secondary["resets_at"])

	return map[string]any{
		"key":   key,
		"label": label,
		"range": map[string]any{"start": start.UnixMilli(), "end": end.UnixMilli()},
		"summary": map[string]any{
			"totalTokens":      totals.Total,
			"totalTokensLabel": fmtInt(totals.Total),
			"inputTokens":      totals.Input,
			"inputLabel":       fmtInt(totals.Input),
			"cachedTokens":     totals.Cached,
			"cachedLabel":      fmtInt(totals.Cached),
			"outputTokens":     totals.Output,
			"outputLabel":      fmtInt(totals.Output),
			"reasoningTokens":  totals.Reasoning,
			"reasoningLabel":   fmtInt(totals.Reasoning),
			"requests":         calls,
			"requestsLabel":    comma(calls),
			"failures":         failureCount,
			"successRate":      successRate,
			"successRateLabel": fmt.Sprintf("%.1f%%", successRate),
			"cacheHit":         cacheHit,
			"cacheHitLabel":    fmt.Sprintf("%.1f%%", cacheHit),
			"failureRate":      failureRate,
			"peakTokens":       peakTotal,
			"peakLabel":        fmtInt(peakTotal),
			"peakTime":         formatPeakLabelForRange(peakTs, hasPeak, start, end),
			"peakTpmLabel":     fmt.Sprintf("%s TPM", fmtInt(peakTotal)),
		},
		"cost": map[string]any{
			"total":            costs.Total,
			"average":          map[bool]float64{true: costs.Total / math.Max(1, float64(calls)), false: 0}[calls > 0],
			"rangeTokensLabel": fmtInt(totals.Total),
			"parts":            buildCostParts(costs),
			"unpricedTokens":   costs.UnpricedTokens,
		},
		"trend":            trendBuckets.compactTrend(),
		"trendStepLabel":   trendBuckets.stepLabel(),
		"trendStepMinutes": trendBuckets.stepMinutes(),
		"distribution":     distributionBuckets.compactDistribution(),
		"sessions":         sessionOut,
		"models":           modelOut,
		"costModels":       costModels,
		"risk": []map[string]any{
			quotaRiskRow("5h 窗口", primaryUsed, primaryReset, hasPrimaryReset, "blue"),
			quotaRiskRow("周限额", secondaryUsed, secondaryReset, hasSecondaryReset, "teal"),
			{"name": "缓存", "value": cacheHit, "label": fmt.Sprintf("命中 %.0f%%", cacheHit), "note": "输入 token", "tone": "teal"},
			{"name": "失败", "value": failureRate, "label": fmt.Sprintf("%.1f%%", failureRate), "note": fmt.Sprintf("%d 次失败", failureCount), "tone": "amber"},
		},
	}
}

func buildPayload(root string, days int, trendMinutes int, cachePath string, cacheFiles map[string]FileCache) map[string]any {
	// Build the public data contract consumed by index.html. Compact array rows
	// are smaller to load than repeated object keys; catalogs retain labels for
	// display without duplicating them in every event record.
	now := time.Now().UTC()
	cutoff := cutoffForDays(days, now)
	loaded := loadSessions(root, cutoff, cachePath, days, cacheFiles)

	sort.SliceStable(loaded.Events, func(i, j int) bool { return loaded.Events[i].Ts.Before(loaded.Events[j].Ts) })
	sort.SliceStable(loaded.TTFBEvents, func(i, j int) bool { return loaded.TTFBEvents[i].Ts.Before(loaded.TTFBEvents[j].Ts) })
	sort.SliceStable(loaded.FailureEvents, func(i, j int) bool { return loaded.FailureEvents[i].Ts.Before(loaded.FailureEvents[j].Ts) })

	sessionCatalog := map[string]map[string]string{}
	for _, session := range loaded.Sessions {
		sessionCatalog[session.Sid] = map[string]string{
			"name":  projectName(session.Cwd, fmt.Sprintf("session %s", tail(session.Sid, 6))),
			"model": session.Model,
		}
	}
	for _, event := range loaded.Events {
		if _, ok := sessionCatalog[event.Sid]; !ok {
			sessionCatalog[event.Sid] = map[string]string{"name": fmt.Sprintf("session %s", tail(event.Sid, 6)), "model": event.Model}
		}
	}

	sessionIDs := make([]string, 0, len(sessionCatalog))
	for sid := range sessionCatalog {
		sessionIDs = append(sessionIDs, sid)
	}
	sort.Strings(sessionIDs)
	sidToIndex := make(map[string]int, len(sessionIDs))
	sessionCatalogRows := make([][]any, 0, len(sessionIDs))
	for index, sid := range sessionIDs {
		sidToIndex[sid] = index
		catalog := sessionCatalog[sid]
		sessionCatalogRows = append(sessionCatalogRows, []any{sid, catalog["name"], catalog["model"]})
	}
	modelToIndex := map[string]int{}
	modelCatalog := make([]string, 0)
	addModel := func(model string) int {
		model = nonEmpty(model, "unknown")
		if index, ok := modelToIndex[model]; ok {
			return index
		}
		index := len(modelCatalog)
		modelToIndex[model] = index
		modelCatalog = append(modelCatalog, model)
		return index
	}
	for _, session := range loaded.Sessions {
		addModel(session.Model)
	}
	for _, event := range loaded.Events {
		addModel(event.Model)
	}
	for _, event := range loaded.TTFBEvents {
		addModel(event.Model)
	}
	for _, event := range loaded.FailureEvents {
		addModel(event.Model)
	}

	dataStart, dataEnd, hasData := loadedDataBounds(loaded, now)
	recordBase := dataStart.UnixMilli()
	recordsV2 := make([][]any, 0, len(loaded.Events))
	for _, event := range loaded.Events {
		sid := nonEmpty(event.Sid, "unknown")
		if _, ok := sidToIndex[sid]; !ok {
			sidToIndex[sid] = len(sessionCatalogRows)
			sessionCatalogRows = append(sessionCatalogRows, []any{sid, fmt.Sprintf("session %s", tail(sid, 6)), nonEmpty(event.Model, "unknown")})
		}
		recordsV2 = append(recordsV2, []any{
			event.Ts.UnixMilli() - recordBase,
			sidToIndex[sid],
			addModel(event.Model),
			event.Usage.Input,
			event.Usage.Cached,
			event.Usage.Output,
			event.Usage.Reasoning,
			event.Usage.Total,
		})
	}
	ttfbRecordsV2 := make([][]any, 0, len(loaded.TTFBEvents))
	for _, event := range loaded.TTFBEvents {
		sid := nonEmpty(event.Sid, "unknown")
		if _, ok := sidToIndex[sid]; !ok {
			sidToIndex[sid] = len(sessionCatalogRows)
			sessionCatalogRows = append(sessionCatalogRows, []any{sid, fmt.Sprintf("session %s", tail(sid, 6)), nonEmpty(event.Model, "unknown")})
		}
		ttfbRecordsV2 = append(ttfbRecordsV2, []any{event.Ts.UnixMilli() - recordBase, sidToIndex[sid], addModel(event.Model), event.TTFBMs})
	}
	failureRecordsV2 := make([][]any, 0, len(loaded.FailureEvents))
	for _, event := range loaded.FailureEvents {
		sid := nonEmpty(event.Sid, "unknown")
		if _, ok := sidToIndex[sid]; !ok {
			sidToIndex[sid] = len(sessionCatalogRows)
			sessionCatalogRows = append(sessionCatalogRows, []any{sid, fmt.Sprintf("session %s", tail(sid, 6)), nonEmpty(event.Model, "unknown")})
		}
		failureRecordsV2 = append(failureRecordsV2, []any{event.Ts.UnixMilli() - recordBase, sidToIndex[sid], addModel(event.Model)})
	}

	availableStart := dataStart.UnixMilli()
	if !hasData && !cutoff.IsZero() {
		availableStart = cutoff.UnixMilli()
	}
	availableEnd := dataEnd.UnixMilli()
	viewNow := dataEnd
	todayStart := localDayStart(viewNow)
	historyStart := dataStart
	viewRanges := []struct {
		key   string
		start time.Time
		end   time.Time
	}{
		{"24h", viewNow.Add(-24 * time.Hour), viewNow},
		{"today", todayStart, viewNow},
		{"7", todayStart.Add(-6 * 24 * time.Hour), viewNow},
		{"30", todayStart.Add(-29 * 24 * time.Hour), viewNow},
		{"history", historyStart, viewNow},
	}
	views := make(map[string]any, len(viewRanges))
	for _, viewRange := range viewRanges {
		views[viewRange.key] = buildView(viewRange.key, formatRangeLabel(viewRange.start, viewRange.end, viewRange.key), viewRange.start, viewRange.end, loaded, sessionCatalog, loaded.Limits)
	}

	primary := mapValue(loaded.Limits, "primary")
	secondary := mapValue(loaded.Limits, "secondary")
	primaryUsed := safePercent(primary["used_percent"])
	secondaryUsed := safePercent(secondary["used_percent"])
	primaryReset, hasPrimaryReset := resetTime(primary["resets_at"])
	secondaryReset, hasSecondaryReset := resetTime(secondary["resets_at"])

	return map[string]any{
		"schemaVersion": 2,
		"generatedAt":   now.Local().Format("2006-01-02 15:04:05"),
		"windowDays":    days,
		"pricingRules":  pricingRulesPayload(),
		"availableRange": map[string]any{
			"start": availableStart,
			"end":   availableEnd,
		},
		"catalog": map[string]any{
			"sessions": sessionCatalogRows,
			"models":   modelCatalog,
		},
		"recordBase":       recordBase,
		"recordsV2":        recordsV2,
		"ttfbRecordsV2":    ttfbRecordsV2,
		"failureRecordsV2": failureRecordsV2,
		"views":            views,
		"limits": map[string]any{
			"limitId":                stringValue(loaded.Limits, "limit_id"),
			"limitName":              stringValue(loaded.Limits, "limit_name"),
			"planType":               nonEmpty(stringValue(loaded.Limits, "plan_type"), "unknown"),
			"primaryUsed":            optionalFloat(primaryUsed),
			"primaryRemaining":       optionalRemaining(primaryUsed),
			"primaryReset":           displayTime(primaryReset, hasPrimaryReset),
			"primaryWindowMinutes":   primary["window_minutes"],
			"secondaryUsed":          optionalFloat(secondaryUsed),
			"secondaryRemaining":     optionalRemaining(secondaryUsed),
			"secondaryReset":         displayTime(secondaryReset, hasSecondaryReset),
			"secondaryWindowMinutes": secondary["window_minutes"],
			"rateLimitReachedType":   loaded.Limits["rate_limit_reached_type"],
		},
	}
}

func buildRawPayload(payload map[string]any) RawExportPayload {
	return RawExportPayload{
		SchemaVersion:    payload["schemaVersion"],
		RawSchemaVersion: 1,
		Catalog:          payload["catalog"],
		RecordBase:       payload["recordBase"],
		RecordsV2:        payload["recordsV2"],
		TTFBRecordsV2:    payload["ttfbRecordsV2"],
		FailureRecordsV2: payload["failureRecordsV2"],
	}
}

func prepareMainPayloadForExport(payload map[string]any, outPath string, rawOutPath string) {
	for _, key := range []string{
		"recordBase",
		"recordsV2",
		"ttfbRecordsV2",
		"failureRecordsV2",
		"catalog",
		"summary",
		"trend",
		"sessions",
		"models",
		"risk",
		"coverage",
	} {
		delete(payload, key)
	}
	payload["rawDataPath"] = browserRawDataPath(outPath, rawOutPath)
}

func browserRawDataPath(outPath string, rawOutPath string) string {
	return browserRelativePath(filepath.Dir(nonEmpty(outPath, ".")), rawOutPath)
}

func browserRelativePath(fromDir string, targetPath string) string {
	if strings.TrimSpace(targetPath) == "" {
		return "data.raw.js"
	}
	if strings.TrimSpace(fromDir) == "" {
		fromDir = "."
	}
	rel, err := filepath.Rel(fromDir, targetPath)
	if err != nil {
		rel = targetPath
	}
	if rel == "." || rel == "" {
		rel = filepath.Base(targetPath)
	}
	return filepath.ToSlash(rel)
}

func payloadLogSummary(payload map[string]any) map[string]any {
	if views, ok := payload["views"].(map[string]any); ok {
		if history, ok := views["history"].(map[string]any); ok {
			if summary, ok := history["summary"].(map[string]any); ok {
				return summary
			}
		}
		if today, ok := views["today"].(map[string]any); ok {
			if summary, ok := today["summary"].(map[string]any); ok {
				return summary
			}
		}
	}
	if summary, ok := payload["summary"].(map[string]any); ok {
		return summary
	}
	return map[string]any{"requestsLabel": "0", "totalTokensLabel": "0"}
}

func mapValue(values map[string]any, key string) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	if value, ok := values[key].(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

func stringValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	if value, ok := values[key].(string); ok {
		return value
	}
	return ""
}

func resetTime(value any) (time.Time, bool) {
	raw, ok := number(value)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(raw), 0).UTC(), true
}

func optionalFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalRemaining(value *float64) any {
	if value == nil {
		return nil
	}
	return 100 - *value
}

func pctLabel(value float64) string {
	return fmt.Sprintf("%.0f%%", clampPercentValue(value))
}

func quotaRiskRow(name string, used *float64, reset time.Time, hasReset bool, tone string) map[string]any {
	if used == nil {
		return map[string]any{
			"name":         name,
			"value":        nil,
			"label":        "等待数据",
			"note":         "本地日志暂无 rate_limits",
			"tone":         tone,
			"percentLabel": "--",
		}
	}
	remaining := clampPercentValue(100 - *used)
	usedLabel := pctLabel(*used)
	return map[string]any{
		"name":         name,
		"value":        remaining,
		"label":        fmt.Sprintf("%s 剩余", pctLabel(remaining)),
		"note":         fmt.Sprintf("已用 %s · %s", usedLabel, displayTime(reset, hasReset)),
		"tone":         tone,
		"percentLabel": pctLabel(remaining),
	}
}

func nonEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func tail(value string, count int) string {
	if len(value) <= count {
		return value
	}
	return value[len(value)-count:]
}

func comma(value int64) string {
	raw := fmt.Sprintf("%d", value)
	if len(raw) <= 3 {
		return raw
	}
	var out []byte
	first := len(raw) % 3
	if first == 0 {
		first = 3
	}
	out = append(out, raw[:first]...)
	for i := first; i < len(raw); i += 3 {
		out = append(out, ',')
		out = append(out, raw[i:i+3]...)
	}
	return string(out)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func defaultRawOutPath(outPath string) string {
	if outPath == "" {
		return "data.raw.js"
	}
	ext := filepath.Ext(outPath)
	if ext == "" {
		return outPath + ".raw"
	}
	return strings.TrimSuffix(outPath, ext) + ".raw" + ext
}

func writeJSPayload(path string, globalName string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	content := append([]byte("window."+globalName+" = "), body...)
	if globalName == "CODEXSCOPE_DATA" {
		content = append(content, []byte(";\nwindow.QUOTASCOPE_DATA = window.CODEXSCOPE_DATA;\n")...)
	} else {
		content = append(content, []byte(";\n")...)
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, content, 0o644)
}

func main() {
	home, _ := os.UserHomeDir()
	defaultRoot := filepath.Join(home, ".codex", "sessions")
	root := flag.String("root", defaultRoot, "Codex sessions directory")
	out := flag.String("out", "data.js", "output data.js path")
	rawOut := flag.String("raw-out", "", "raw event sidecar path; defaults to data.raw.js next to the main output")
	days := flag.Int("days", 0, "number of days to include; 0 means all history")
	trendMinutes := flag.Int("trend-minutes", 300, "minutes in the summary trend")
	cache := flag.String("cache", ".codexscope-cache.json", "local cache path")
	noCache := flag.Bool("no-cache", false, "disable local cache")
	flag.Parse()

	cachePath := *cache
	if *noCache {
		cachePath = ""
	}
	rawOutPath := *rawOut
	if rawOutPath == "" {
		rawOutPath = defaultRawOutPath(*out)
	}
	stampPath := cacheStampPath(cachePath)
	stampSignature := ""
	var cacheFiles map[string]FileCache
	if cachePath != "" {
		now := time.Now().UTC()
		cutoff := cutoffForDays(*days, now)
		files := collectSessionFiles(*root, cutoff)
		stampSignature = fileSignature(*root, *out, rawOutPath, *days, *trendMinutes, files)
		if outputIsStampedFresh(*out, rawOutPath, files, stampPath, stampSignature) {
			fmt.Printf("%s is up to date (%d files)\n", *out, len(files))
			return
		}
		cacheFiles = loadCache(cachePath, *days)
		if outputIsFresh(*out, rawOutPath, files, cacheFiles) {
			fmt.Printf("%s is up to date (%d cached files)\n", *out, len(files))
			writeRunStamp(stampPath, stampSignature)
			return
		}
	}
	payload := buildPayload(*root, *days, *trendMinutes, cachePath, cacheFiles)
	summary := payloadLogSummary(payload)
	rawPayload := buildRawPayload(payload)
	prepareMainPayloadForExport(payload, *out, rawOutPath)
	if err := writeJSPayload(rawOutPath, "CODEXSCOPE_RAW_DATA", rawPayload); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write %s: %v\n", rawOutPath, err)
		os.Exit(1)
	}
	if err := writeJSPayload(*out, "CODEXSCOPE_DATA", payload); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write %s: %v\n", *out, err)
		os.Exit(1)
	}
	writeRunStamp(stampPath, stampSignature)
	fmt.Printf("wrote %s (%s requests, %s tokens)\n", *out, summary["requestsLabel"], summary["totalTokensLabel"])
}
