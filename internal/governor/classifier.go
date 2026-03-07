package governor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/inth3shadows/runecho/internal/schema"
)

const cacheMaxSize = 64
const cacheTTL = 30 * time.Minute

type cacheEntry struct {
	route Route
	ts    time.Time
}

var (
	classifierCache     = make(map[string]cacheEntry, cacheMaxSize)
	classifierCacheMu   sync.Mutex
	classifierCacheKeys []string // insertion-order list for LRU eviction
)

// PromptFingerprint returns a 16-hex-char key derived from the first 200 chars of prompt.
func PromptFingerprint(prompt string) string {
	p := prompt
	if len(p) > 200 {
		p = p[:200]
	}
	h := sha256.Sum256([]byte(p))
	return hex.EncodeToString(h[:])[:16]
}

func cacheGet(fingerprint string) (Route, bool) {
	classifierCacheMu.Lock()
	defer classifierCacheMu.Unlock()
	e, ok := classifierCache[fingerprint]
	if !ok {
		return "", false
	}
	if time.Since(e.ts) > cacheTTL {
		return "", false
	}
	return e.route, true
}

func cacheSet(fingerprint string, route Route) {
	classifierCacheMu.Lock()
	defer classifierCacheMu.Unlock()
	if len(classifierCacheKeys) >= cacheMaxSize {
		evict := classifierCacheKeys[0]
		classifierCacheKeys = classifierCacheKeys[1:]
		delete(classifierCache, evict)
	}
	classifierCache[fingerprint] = cacheEntry{route: route, ts: time.Now()}
	classifierCacheKeys = append(classifierCacheKeys, fingerprint)
}

const (
	classifierModel   = "claude-haiku-4-5-20251001"
	classifierAPIURL  = "https://api.anthropic.com/v1/messages"
	classifierVersion = "2023-06-01"
	classifierTimeout = 2 * time.Second
	classifierSystem  = `Classify the prompt as exactly one: haiku, sonnet, opus, pipeline.
haiku: read-only tasks (search, summarize, explain, find, describe, document, write handoff)
sonnet: direct code tasks (fix bug, refactor, write tests, edit file, rename)
opus: reasoning tasks (architecture, design, review, trade-offs, strategy, feasibility, alignment, is this right)
pipeline: multi-phase implementation (build new feature, implement from scratch, scaffold, end-to-end)
Respond with JSON only: {"route":"haiku|sonnet|opus|pipeline"}`
)

// Classify calls the haiku LLM to classify prompt intent.
// Returns ("", 0) if the key is absent, the call fails, or the result is invalid.
// Cache hits skip the API call; results are cached for 30 minutes (max 64 entries).
func Classify(prompt, apiKey, stateDir string) (Route, int64) {
	if apiKey == "" {
		return "", 0
	}

	prompt200 := prompt
	if len(prompt200) > 200 {
		prompt200 = prompt200[:200]
	}

	fingerprint := PromptFingerprint(prompt200)

	if cached, ok := cacheGet(fingerprint); ok {
		logClassification(prompt200, cached, 0, stateDir, nil, true)
		return cached, 0
	}

	start := time.Now()
	route, err := classifyCall(prompt200, apiKey)
	latencyMS := time.Since(start).Milliseconds()

	if err == nil {
		cacheSet(fingerprint, route)
	}

	logClassification(prompt200, route, latencyMS, stateDir, err, false)

	if err != nil {
		return "", latencyMS
	}
	return route, latencyMS
}

func classifyCall(prompt, apiKey string) (Route, error) {
	reqBody, err := json.Marshal(map[string]any{
		"model":      classifierModel,
		"max_tokens": 20,
		"system":     classifierSystem,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", classifierAPIURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", classifierVersion)
	req.Header.Set("content-type", "application/json")

	client := &http.Client{Timeout: classifierTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("classifier API %d", resp.StatusCode)
	}

	var apiResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", err
	}
	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("empty response")
	}

	var result struct {
		Route string `json:"route"`
	}
	if err := json.Unmarshal([]byte(apiResp.Content[0].Text), &result); err != nil {
		return "", err
	}

	route := Route(strings.TrimSpace(result.Route))
	switch route {
	case RouteHaiku, RouteSonnet, RouteOpus, RoutePipeline:
		return route, nil
	default:
		return "", fmt.Errorf("unknown route: %q", route)
	}
}

func logClassification(prompt string, route Route, latencyMS int64, stateDir string, callErr error, cacheHit bool) {
	if stateDir == "" {
		return
	}
	routeStr := string(route)
	if callErr != nil || route == "" {
		routeStr = ""
	}

	source := "classifier"
	if cacheHit {
		source = "cache"
	}

	entry := schema.ClassifierEntry{
		Ts:        time.Now().UTC().Format(time.RFC3339),
		Prompt:    prompt,
		Route:     routeStr,
		Source:    source,
		LatencyMS: latencyMS,
		CacheHit:  cacheHit,
	}
	if callErr != nil {
		entry.Error = callErr.Error()
	}

	line, err := json.Marshal(entry)
	if err != nil {
		return
	}

	logFile := filepath.Join(stateDir, "classifier-log.jsonl")
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s\n", line)
}
