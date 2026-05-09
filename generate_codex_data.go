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

const cacheVersion = 3
const minReadableCacheVersion = 3

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
	Ts    string `json:"ts"`
	Sid   string `json:"sid"`
	Usage Usage  `json:"usage"`
	Model string `json:"model"`
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

func fmtDuration(seconds *float64) string {
	if seconds == nil {
		return "未知"
	}
	sec := int64(math.Max(0, *seconds))
	hours := sec / 3600
	minutes := (sec % 3600) / 60
	if hours > 0 {
		return fmt.Sprintf("%dh %02dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
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

func percentValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func cutoffForDays(days int, now time.Time) time.Time {
	if days <= 0 {
		return time.Time{}
	}
	return now.Add(-time.Duration(days) * 24 * time.Hour)
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

func fileSignature(root string, out string, days int, trendMinutes int, files []sessionFileCandidate) string {
	var totalSize int64
	var maxMtimeNs int64
	for _, file := range files {
		totalSize += file.size
		if file.mtimeNs > maxMtimeNs {
			maxMtimeNs = file.mtimeNs
		}
	}
	return fmt.Sprintf("v=%d\nroot=%s\nout=%s\ndays=%d\ntrend=%d\nfiles=%d:%d:%d\n",
		cacheVersion, root, out, days, trendMinutes, len(files), totalSize, maxMtimeNs)
}

func outputIsStampedFresh(outPath string, files []sessionFileCandidate, stampPath string, expectedSignature string) bool {
	if outPath == "" || stampPath == "" {
		return false
	}
	outInfo, err := os.Stat(outPath)
	if err != nil || outInfo.IsDir() || sourceNewerThan(outInfo.ModTime()) {
		return false
	}
	stampInfo, err := os.Stat(stampPath)
	if err != nil || stampInfo.IsDir() || stampInfo.ModTime().Before(outInfo.ModTime()) {
		return false
	}
	body, err := os.ReadFile(stampPath)
	if err != nil {
		return false
	}
	if string(body) != expectedSignature {
		return false
	}
	outMtime := outInfo.ModTime()
	for _, file := range files {
		if time.Unix(0, file.mtimeNs).After(outMtime) {
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
						parsed.UsageEvents = append(parsed.UsageEvents, UsageEvent{Ts: isoTime(ts, true), Sid: parsed.Sid, Usage: usage, Model: parsed.Model})
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

func mergeSessionFile(parsed ParsedFile, cutoff time.Time, loaded *LoadedData, latestLimitsTs *time.Time) {
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
		markSeen(&stat, ts)
		addUsage(&stat.Usage, event.Usage)
		stat.Calls++
		eventSid := event.Sid
		if eventSid == "" {
			eventSid = sid
		}
		eventModel := event.Model
		if eventModel == "" {
			eventModel = model
		}
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

func loadSessions(root string, cutoff time.Time, cachePath string, days int) LoadedData {
	loaded := LoadedData{}
	cacheFiles := loadCache(cachePath, days)
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
	for _, result := range parsedFiles {
		nextCacheFiles[result.file.path] = FileCache{MtimeNs: result.file.mtimeNs, Size: result.file.size, Parsed: result.parsed}
		mergeSessionFile(result.parsed, cutoff, &loaded, &latestLimitsTs)
	}

	writeCache(cachePath, days, nextCacheFiles)
	return loaded
}

func outputIsFresh(outPath string, files []sessionFileCandidate, cacheFiles map[string]FileCache) bool {
	if outPath == "" || len(files) == 0 || len(cacheFiles) != len(files) {
		return false
	}
	outInfo, err := os.Stat(outPath)
	if err != nil || outInfo.IsDir() {
		return false
	}
	if sourceInfo, err := os.Stat("generate_codex_data.go"); err == nil && outInfo.ModTime().Before(sourceInfo.ModTime()) {
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

func bucketEvents(events []RuntimeEvent, now time.Time, minutes int) []map[string]any {
	// This precomputed trend is retained for compatibility with older exports.
	// The current UI also keeps raw records so date filters can recompute buckets
	// in the browser without rerunning the generator.
	bucketCount := 11
	end := now.Truncate(time.Minute)
	start := end.Add(-time.Duration(minutes) * time.Minute)
	step := float64(minutes) / float64(bucketCount-1)
	buckets := make([]struct {
		start time.Time
		end   time.Time
		usage Usage
	}, bucketCount)
	for i := range buckets {
		bStart := start.Add(time.Duration(math.Round(float64(i)*step)) * time.Minute)
		bEnd := end.Add(time.Second)
		if i < bucketCount-1 {
			bEnd = start.Add(time.Duration(math.Round(float64(i+1)*step)) * time.Minute)
		}
		buckets[i].start = bStart
		buckets[i].end = bEnd
	}
	for _, event := range events {
		if event.Ts.Before(start) || event.Ts.After(end) {
			continue
		}
		idx := int(((event.Ts.Sub(start)).Minutes()) / step)
		if idx < 0 {
			idx = 0
		}
		if idx >= bucketCount {
			idx = bucketCount - 1
		}
		addUsage(&buckets[idx].usage, event.Usage)
	}
	rows := make([]map[string]any, 0, len(buckets))
	for _, bucket := range buckets {
		rows = append(rows, map[string]any{
			"label":     displayTime(bucket.start, true),
			"input":     bucket.usage.Input,
			"cached":    bucket.usage.Cached,
			"output":    bucket.usage.Output,
			"reasoning": bucket.usage.Reasoning,
			"total":     bucket.usage.Total,
		})
	}
	return rows
}

func peakRate(events []RuntimeEvent) (int64, time.Time, bool) {
	// Peak TPM is a sliding one-minute sum over chronological token events.
	window := time.Minute
	left := 0
	var total int64
	var peakTotal int64
	var peakTs time.Time
	var hasPeak bool
	for right, event := range events {
		total += event.Usage.Total
		for left <= right && event.Ts.Sub(events[left].Ts) >= window {
			total -= events[left].Usage.Total
			left++
		}
		if total > peakTotal {
			peakTotal = total
			peakTs = event.Ts
			hasPeak = true
		}
	}
	return peakTotal, peakTs, hasPeak
}

func buildPayload(root string, days int, trendMinutes int, cachePath string) map[string]any {
	// Build the public data contract consumed by index.html. Compact array rows
	// are smaller to load than repeated object keys; catalogs retain labels for
	// display without duplicating them in every event record.
	now := time.Now().UTC()
	cutoff := cutoffForDays(days, now)
	loaded := loadSessions(root, cutoff, cachePath, days)

	sort.SliceStable(loaded.Events, func(i, j int) bool { return loaded.Events[i].Ts.Before(loaded.Events[j].Ts) })
	sort.SliceStable(loaded.TTFBEvents, func(i, j int) bool { return loaded.TTFBEvents[i].Ts.Before(loaded.TTFBEvents[j].Ts) })
	sort.SliceStable(loaded.FailureEvents, func(i, j int) bool { return loaded.FailureEvents[i].Ts.Before(loaded.FailureEvents[j].Ts) })

	var totals Usage
	for _, event := range loaded.Events {
		addUsage(&totals, event.Usage)
	}

	byModel := map[string]map[string]any{}
	for _, session := range loaded.Sessions {
		model := session.Model
		if model == "" {
			model = "unknown"
		}
		row := byModel[model]
		if row == nil {
			row = map[string]any{"tokens": int64(0), "requests": int64(0), "ttfb_ms": int64(0), "ttfb_count": int64(0)}
			byModel[model] = row
		}
		row["tokens"] = row["tokens"].(int64) + session.Usage.Total
		row["requests"] = row["requests"].(int64) + session.Calls
		row["ttfb_ms"] = row["ttfb_ms"].(int64) + session.TTFBMs
		row["ttfb_count"] = row["ttfb_count"].(int64) + session.TTFBCount
	}

	sort.Slice(loaded.Sessions, func(i, j int) bool { return loaded.Sessions[i].Usage.Total > loaded.Sessions[j].Usage.Total })
	sessionLimit := min(len(loaded.Sessions), 20)
	sessionRows := make([]map[string]any, 0, sessionLimit)
	for i := 0; i < sessionLimit; i++ {
		session := loaded.Sessions[i]
		var duration *float64
		if session.DurationMs > 0 {
			sec := float64(session.DurationMs) / 1000
			duration = &sec
		}
		sessionRows = append(sessionRows, map[string]any{
			"rank":        i + 1,
			"name":        projectName(session.Cwd, fmt.Sprintf("session %s", tail(session.Sid, 6))),
			"model":       session.Model,
			"tokens":      session.Usage.Total,
			"tokensLabel": fmtInt(session.Usage.Total),
			"requests":    session.Calls,
			"duration":    fmtDuration(duration),
			"status":      map[bool]string{true: "ok", false: "warn"}[session.Failures == 0],
		})
	}
	var maxSessionTokens int64 = 1
	for _, row := range sessionRows {
		if tokens := row["tokens"].(int64); tokens > maxSessionTokens {
			maxSessionTokens = tokens
		}
	}
	for _, row := range sessionRows {
		row["percent"] = int(math.Round(float64(row["tokens"].(int64)) / float64(maxSessionTokens) * 100))
	}

	modelNames := make([]string, 0, len(byModel))
	for name := range byModel {
		modelNames = append(modelNames, name)
	}
	sort.Slice(modelNames, func(i, j int) bool {
		return byModel[modelNames[i]]["tokens"].(int64) > byModel[modelNames[j]]["tokens"].(int64)
	})
	modelLimit := min(len(modelNames), 12)
	modelRows := make([]map[string]any, 0, modelLimit)
	var maxModelTokens int64 = 1
	for i := 0; i < modelLimit; i++ {
		name := modelNames[i]
		row := byModel[name]
		tokens := row["tokens"].(int64)
		if tokens > maxModelTokens {
			maxModelTokens = tokens
		}
		latencyCount := row["ttfb_count"].(int64)
		latency := 0.0
		latencyLabel := "--"
		if latencyCount > 0 {
			latency = float64(row["ttfb_ms"].(int64)) / float64(latencyCount) / 1000
			latencyLabel = fmt.Sprintf("%.2fs", latency)
		}
		modelRows = append(modelRows, map[string]any{
			"name":         name,
			"tokens":       tokens,
			"tokensLabel":  fmtInt(tokens),
			"requests":     row["requests"].(int64),
			"latency":      latency,
			"latencyLabel": latencyLabel,
		})
	}
	for _, row := range modelRows {
		row["percent"] = int(math.Round(float64(row["tokens"].(int64)) / float64(maxModelTokens) * 100))
	}

	trend := bucketEvents(loaded.Events, now, trendMinutes)
	peakTotal, peakTs, hasPeak := peakRate(loaded.Events)

	primary := mapValue(loaded.Limits, "primary")
	secondary := mapValue(loaded.Limits, "secondary")
	primaryUsed := safePercent(primary["used_percent"])
	secondaryUsed := safePercent(secondary["used_percent"])
	primaryReset, hasPrimaryReset := resetTime(primary["resets_at"])
	secondaryReset, hasSecondaryReset := resetTime(secondary["resets_at"])

	var calls int64
	var failures int64
	for _, session := range loaded.Sessions {
		calls += session.Calls
		failures += session.Failures
	}
	successRate := 100.0
	failureRate := 0.0
	if calls > 0 {
		successRate = math.Max(0, math.Min(100, float64(calls-failures)/float64(calls)*100))
		failureRate = math.Max(0, math.Min(100, float64(failures)/float64(calls)*100))
	}
	cacheHit := 0.0
	if totals.Input > 0 {
		cacheHit = float64(totals.Cached) / float64(totals.Input) * 100
	}

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

	records := make([][]any, 0, len(loaded.Events))
	for _, event := range loaded.Events {
		records = append(records, []any{
			event.Ts.UnixMilli(),
			event.Sid,
			nonEmpty(event.Model, "unknown"),
			event.Usage.Input,
			event.Usage.Cached,
			event.Usage.Output,
			event.Usage.Reasoning,
			event.Usage.Total,
		})
	}
	ttfbRecords := make([][]any, 0, len(loaded.TTFBEvents))
	for _, event := range loaded.TTFBEvents {
		ttfbRecords = append(ttfbRecords, []any{event.Ts.UnixMilli(), event.Sid, nonEmpty(event.Model, "unknown"), event.TTFBMs})
	}
	failureRecords := make([][]any, 0, len(loaded.FailureEvents))
	for _, event := range loaded.FailureEvents {
		failureRecords = append(failureRecords, []any{event.Ts.UnixMilli(), event.Sid, nonEmpty(event.Model, "unknown")})
	}

	availableStart := now.UnixMilli()
	if !cutoff.IsZero() {
		availableStart = cutoff.UnixMilli()
	}
	availableEnd := now.UnixMilli()
	if len(records) > 0 {
		availableStart = records[0][0].(int64)
		availableEnd = records[len(records)-1][0].(int64)
	}

	return map[string]any{
		"generatedAt": now.Local().Format("2006-01-02 15:04:05"),
		"windowDays":  days,
		"availableRange": map[string]any{
			"start": availableStart,
			"end":   availableEnd,
		},
		"sessionsCatalog": sessionCatalog,
		"records":         records,
		"ttfbRecords":     ttfbRecords,
		"failureRecords":  failureRecords,
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
			"requestsLabel":    fmt.Sprintf("%s", comma(calls)),
			"failures":         failures,
			"successRate":      successRate,
			"successRateLabel": fmt.Sprintf("%.1f%%", successRate),
			"cacheHit":         cacheHit,
			"cacheHitLabel":    fmt.Sprintf("%.1f%%", cacheHit),
			"peakTokens":       peakTotal,
			"peakLabel":        fmtInt(peakTotal),
			"peakTime":         displayTime(peakTs, hasPeak),
			"peakTpmLabel":     fmt.Sprintf("%s TPM", fmtInt(peakTotal)),
		},
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
		"trend":    trend,
		"sessions": sessionRows,
		"models":   modelRows,
		"risk": []map[string]any{
			{"name": "5h", "value": percentValue(primaryUsed), "label": fmt.Sprintf("%.0f%% used", percentValue(primaryUsed)), "limit": "Codex primary"},
			{"name": "Week", "value": percentValue(secondaryUsed), "label": fmt.Sprintf("%.0f%% used", percentValue(secondaryUsed)), "limit": "Codex secondary"},
			{"name": "Cache", "value": cacheHit, "label": fmt.Sprintf("%.0f%% hit", cacheHit), "limit": "local logs"},
			{"name": "Fail", "value": failureRate, "label": fmt.Sprintf("%d (%.1f%%)", failures, failureRate), "limit": "errors"},
		},
		"coverage": []map[string]any{
			{"metric": "真实额度", "source": "token_count.rate_limits", "status": okMissing(loaded.Limits != nil)},
			{"metric": "Token 消耗", "source": "token_count.last_token_usage", "status": okMissing(len(loaded.Events) > 0)},
			{"metric": "会话排行", "source": "session_meta + token_count", "status": okMissing(len(loaded.Sessions) > 0)},
			{"metric": "模型排行", "source": "turn_context.model", "status": okMissing(len(modelRows) > 0)},
			{"metric": "峰值速率", "source": "selected range buckets", "status": okMissing(len(loaded.Events) > 0)},
			{"metric": "缓存命中", "source": "cached_input_tokens / input_tokens", "status": okMissing(totals.Input > 0)},
		},
	}
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

func okMissing(ok bool) string {
	if ok {
		return "ok"
	}
	return "missing"
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

func main() {
	home, _ := os.UserHomeDir()
	defaultRoot := filepath.Join(home, ".codex", "sessions")
	root := flag.String("root", defaultRoot, "Codex sessions directory")
	out := flag.String("out", "data.js", "output data.js path")
	days := flag.Int("days", 0, "number of days to include; 0 means all history")
	trendMinutes := flag.Int("trend-minutes", 300, "minutes in the summary trend")
	cache := flag.String("cache", ".codexscope-cache.json", "local cache path")
	noCache := flag.Bool("no-cache", false, "disable local cache")
	flag.Parse()

	cachePath := *cache
	if *noCache {
		cachePath = ""
	}
	stampPath := cacheStampPath(cachePath)
	stampSignature := ""
	if cachePath != "" {
		now := time.Now().UTC()
		cutoff := cutoffForDays(*days, now)
		files := collectSessionFiles(*root, cutoff)
		stampSignature = fileSignature(*root, *out, *days, *trendMinutes, files)
		if outputIsStampedFresh(*out, files, stampPath, stampSignature) {
			fmt.Printf("%s is up to date (%d files)\n", *out, len(files))
			return
		}
		cacheFiles := loadCache(cachePath, *days)
		if outputIsFresh(*out, files, cacheFiles) {
			fmt.Printf("%s is up to date (%d cached files)\n", *out, len(files))
			writeRunStamp(stampPath, stampSignature)
			return
		}
	}
	payload := buildPayload(*root, *days, *trendMinutes, cachePath)
	body, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to encode payload: %v\n", err)
		os.Exit(1)
	}
	content := append([]byte("window.CODEXSCOPE_DATA = "), body...)
	content = append(content, []byte(";\nwindow.QUOTASCOPE_DATA = window.CODEXSCOPE_DATA;\n")...)
	if err := os.WriteFile(*out, content, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write %s: %v\n", *out, err)
		os.Exit(1)
	}
	writeRunStamp(stampPath, stampSignature)
	summary := payload["summary"].(map[string]any)
	fmt.Printf("wrote %s (%s requests, %s tokens)\n", *out, summary["requestsLabel"], summary["totalTokensLabel"])
}
