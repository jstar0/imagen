package imagen

import (
	"encoding/json"
	"strings"
)

// A returned gateway/server error is distinct from a client wait timeout or a
// broken transport with no terminal response. Never route around safety/input errors.
func terminalChannelFailure(status int, body []byte) bool {
	text := strings.ToLower(string(body))
	for _, word := range []string{"moderation", "safety", "content_policy", "content policy", "content_filter", "policy_violation", "cyber", "blocked_prompt", "invalid_request", "invalid request", "invalid_parameter", "invalid parameter"} {
		if strings.Contains(text, word) {
			return false
		}
	}
	if status >= 500 && status <= 599 {
		return true
	}
	var p struct {
		Type     string          `json:"type"`
		Error    json.RawMessage `json:"error"`
		Response struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(body, &p) != nil {
		return false
	}
	raw := p.Error
	if len(raw) == 0 && p.Type == "error" {
		raw = body
	}
	if len(raw) == 0 {
		raw = p.Response.Error
	}
	var obj struct {
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	for _, code := range []string{obj.Code, obj.Type} {
		if oneOf(strings.ToLower(code), "internal_server_error", "server_error", "upstream_error", "upstream_truncated", "upstream_unreachable", "upstream_proxy_failed", "service_unavailable", "gateway_timeout") {
			return true
		}
	}
	return false
}
