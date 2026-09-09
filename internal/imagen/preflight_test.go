package imagen

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestWaitTimeoutRejectedBeforeConfigOrSubmission(t *testing.T) {
	for _, value := range []string{"-1", "3601", "NaN", "+Inf"} {
		cmd := NewCommand()
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		cmd.SetArgs([]string{"--config", filepath.Join(t.TempDir(), "absent.json"), "generate", "--prompt", "test", "--wait", "--timeout", value})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "timeout must") {
			t.Fatalf("%s: expected timeout preflight rejection, got %v", value, err)
		}
	}
}
