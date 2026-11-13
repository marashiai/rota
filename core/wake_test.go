package core

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// bashShift writes the transcript of the agent's shift as of now: one command, and what it printed.
func bashShift(t *testing.T, root, agent, command, output string) {
	t.Helper()
	use, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]string{"command": command}}}}})
	result, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": output}}}})
	now := time.Now()
	write(t, filepath.Join(root, "logs", now.Format("2006-01-02"), now.Format("1504")+"-"+agent+".jsonl"), string(use)+"\n"+string(result)+"\n")
}
