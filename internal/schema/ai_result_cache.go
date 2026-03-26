package schema

// AIResultCacheEntry represents one cached AI response in the result cache DB.
// Key: (ir_hash, prompt_hash, model, task_id).
type AIResultCacheEntry struct {
	IRHash     string `json:"ir_hash"`
	PromptHash string `json:"prompt_hash"`
	Model      string `json:"model"`
	TaskID     string `json:"task_id"`
	Result     string `json:"result"`
	CreatedAt  string `json:"created_at"`
}
