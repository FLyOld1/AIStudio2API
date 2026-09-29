// Package requestlog 将公开 API 请求的入参与出参落盘为 JSONL 文件
package requestlog

import "time"

// Usage 表示单次请求的 token 用量
type Usage struct {
	InputTokens     int64 `json:"input_tokens,omitempty"`
	OutputTokens    int64 `json:"output_tokens,omitempty"`
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty"`
	ToolTokens      int64 `json:"tool_tokens,omitempty"`
	TotalTokens     int64 `json:"total_tokens,omitempty"`
}

// Entry 表示一条完整的请求日志，包含入参正文与出参正文
type Entry struct {
	Time          time.Time `json:"time"`
	RequestID     string    `json:"request_id,omitempty"`
	ClientIP      string    `json:"client_ip,omitempty"`
	RemoteAddr    string    `json:"remote_addr,omitempty"`
	Method        string    `json:"method,omitempty"`
	Path          string    `json:"path,omitempty"`
	Query         string    `json:"query,omitempty"`
	Model         string    `json:"model,omitempty"`
	Account       string    `json:"account,omitempty"`
	Channel       string    `json:"channel,omitempty"`
	Status        int       `json:"status"`
	LatencyMS     float64   `json:"latency_ms,omitempty"`
	FirstEventMS  float64   `json:"first_event_ms,omitempty"`
	UpstreamBytes int64     `json:"upstream_bytes,omitempty"`
	Usage         *Usage    `json:"usage,omitempty"`
	FinishReason  string    `json:"finish_reason,omitempty"`
	Error         string    `json:"error,omitempty"`
	Canceled      bool      `json:"canceled,omitempty"`
	Generation    bool      `json:"generation,omitempty"`

	RequestBody          string `json:"request_body,omitempty"`
	RequestBodyBytes     int64  `json:"request_body_bytes,omitempty"`
	RequestBodyTruncated bool   `json:"request_body_truncated,omitempty"`

	ResponseBody          string `json:"response_body,omitempty"`
	ResponseBodyBytes     int64  `json:"response_body_bytes,omitempty"`
	ResponseBodyTruncated bool   `json:"response_body_truncated,omitempty"`
}
