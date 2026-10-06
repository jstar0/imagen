package imagen

import "testing"

func TestTerminalChannelFailure(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{200, `{"error":{"code":"internal_server_error","message":"Post upstream: EOF"}}`, true},
		{200, `{"type":"error","code":"server_error","message":"connection lost"}`, true},
		{200, `{"type":"response.failed","response":{"error":{"code":"server_error"}}}`, true},
		{200, `{"type":"response.failed","response":{"error":{"code":"content_policy_violation"}}}`, false},
		{504, `gateway timeout`, true},
		{500, `{"error":{"code":"invalid_request_error"}}`, false},
		{200, `{"data":[]}`, false},
		{200, `EOF`, false},
	} {
		if got := terminalChannelFailure(tc.status, []byte(tc.body)); got != tc.want {
			t.Fatalf("%d %s: %t", tc.status, tc.body, got)
		}
	}
}
