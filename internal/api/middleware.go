package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

type accessLogContextKey struct{}

type accessLogMetadata struct {
	mu              sync.Mutex
	admin           AdminService
	method          string
	path            string
	started         bool
	generation      bool
	model           string
	account         string
	channel         string
	finishReason    string
	err             string
	canceled        bool
	failureStatus   int
	firstEvent      time.Duration
	upstreamBytes   int64
	usage           *aistudio.Usage
	toolCalls       int
	inputMessages   int
	inputTextChars  int
	inputMedia      int
	inputMediaBytes int64
	inputFiles      int
	temperature     string
	topP            string
	thinking        string
	maxOutputTokens string
	requestID       string
}

type accessLogSnapshot struct {
	generation      bool
	model           string
	account         string
	channel         string
	finishReason    string
	requestErr      string
	canceled        bool
	failureStatus   int
	firstEvent      time.Duration
	upstreamBytes   int64
	usage           *aistudio.Usage
	toolCalls       int
	inputMessages   int
	inputTextChars  int
	inputMedia      int
	inputMediaBytes int64
	inputFiles      int
	temperature     string
	topP            string
	thinking        string
	maxOutputTokens string
	requestID       string
}

type accessLogResponseWriter struct {
	http.ResponseWriter
	status   int
	metadata *accessLogMetadata
	capture  *captureBuffer
}

func (writer *accessLogResponseWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *accessLogResponseWriter) Write(data []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	writer.capture.append(data)
	return writer.ResponseWriter.Write(data)
}

// FlushError 记录状态并向响应控制器返回底层刷新错误
func (writer *accessLogResponseWriter) FlushError() error {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(writer.ResponseWriter).Flush()
}

func (writer *accessLogResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func (writer *accessLogResponseWriter) setError(message string) {
	writer.metadata.setError(message)
}

func (metadata *accessLogMetadata) setTarget(model string, account string) {
	metadata.mu.Lock()
	if model = strings.TrimSpace(model); model != "" {
		metadata.model = strings.TrimPrefix(model, "models/")
	}
	if account = strings.TrimSpace(account); account != "" {
		metadata.account = account
	}
	metadata.mu.Unlock()
	metadata.start(false)
}

func (metadata *accessLogMetadata) start(force bool) {
	metadata.mu.Lock()
	if metadata.started || !force && metadata.account == "" {
		metadata.mu.Unlock()
		return
	}
	metadata.started = true
	admin := metadata.admin
	entry := AccessLog{
		Method: metadata.method, Path: metadata.path, Model: metadata.model, Account: metadata.account, Channel: metadata.channel,
		Temperature: metadata.temperature, TopP: metadata.topP, Thinking: metadata.thinking,
		MaxOutputTokens: metadata.maxOutputTokens, Generation: metadata.generation, RequestID: metadata.requestID,
		InputMessages: metadata.inputMessages, InputTextChars: metadata.inputTextChars,
		InputMedia: metadata.inputMedia, InputMediaBytes: metadata.inputMediaBytes, InputFiles: metadata.inputFiles,
	}
	metadata.mu.Unlock()
	if admin != nil {
		admin.RecordAccessStart(entry)
	}
}

func (metadata *accessLogMetadata) setError(message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	metadata.mu.Lock()
	metadata.err = message
	metadata.mu.Unlock()
}

func (metadata *accessLogMetadata) setRequestError(err error) {
	if err == nil {
		return
	}
	metadata.mu.Lock()
	metadata.err = strings.TrimSpace(err.Error())
	metadata.canceled = errors.Is(err, context.Canceled)
	if metadata.canceled {
		metadata.failureStatus = 499
	} else {
		metadata.failureStatus = statusFromError(err)
	}
	metadata.mu.Unlock()
}

func (metadata *accessLogMetadata) setFinishReason(reason string) {
	metadata.mu.Lock()
	metadata.finishReason = strings.TrimSpace(reason)
	metadata.mu.Unlock()
}

func (metadata *accessLogMetadata) setGenerationResult(
	usage *aistudio.Usage,
	toolCalls int,
) {
	metadata.mu.Lock()
	if usage != nil {
		value := *usage
		metadata.usage = &value
	}
	metadata.toolCalls = toolCalls
	metadata.mu.Unlock()
}

func (metadata *accessLogMetadata) setGenerationConfig(config aistudio.GenerationConfig) {
	metadata.mu.Lock()
	metadata.generation = true
	metadata.temperature = formatLogFloat(config.Temperature)
	metadata.topP = formatLogFloat(config.TopP)
	metadata.thinking = formatLogThinking(config)
	metadata.maxOutputTokens = formatLogInt(config.MaxOutputTokens)
	metadata.mu.Unlock()
}

func (metadata *accessLogMetadata) setGenerationInput(request aistudio.GenerateRequest) {
	textChars := utf8.RuneCountInString(request.System)
	media := 0
	var mediaBytes int64
	files := 0
	for _, content := range request.Contents {
		for _, part := range content.Parts {
			textChars += utf8.RuneCountInString(part.Text)
			if part.InlineData != nil {
				media++
				mediaBytes += int64(len(part.InlineData.Data))
			}
			if part.ExternalMedia != nil {
				media++
			}
			if part.File != nil {
				files++
			}
		}
	}
	metadata.mu.Lock()
	metadata.requestID = strings.TrimSpace(request.ID)
	metadata.inputMessages = len(request.Contents)
	metadata.inputTextChars = textChars
	metadata.inputMedia = media
	metadata.inputMediaBytes = mediaBytes
	metadata.inputFiles = files
	metadata.mu.Unlock()
}

func (metadata *accessLogMetadata) setUpstreamBytes(bytes int64) {
	metadata.mu.Lock()
	metadata.upstreamBytes = bytes
	metadata.mu.Unlock()
}

func (metadata *accessLogMetadata) setFirstEvent(firstEvent time.Duration) {
	metadata.mu.Lock()
	if metadata.firstEvent == 0 {
		metadata.firstEvent = firstEvent
	}
	metadata.mu.Unlock()
}

func (metadata *accessLogMetadata) snapshot() accessLogSnapshot {
	metadata.mu.Lock()
	snapshot := accessLogSnapshot{
		generation: metadata.generation,
		model:      metadata.model, account: metadata.account, channel: metadata.channel, finishReason: metadata.finishReason,
		requestErr: metadata.err, canceled: metadata.canceled,
		failureStatus: metadata.failureStatus,
		firstEvent:    metadata.firstEvent,
		upstreamBytes: metadata.upstreamBytes, usage: metadata.usage, toolCalls: metadata.toolCalls,
		inputMessages: metadata.inputMessages, inputTextChars: metadata.inputTextChars,
		inputMedia: metadata.inputMedia, inputMediaBytes: metadata.inputMediaBytes, inputFiles: metadata.inputFiles,
		temperature: metadata.temperature, topP: metadata.topP,
		thinking: metadata.thinking, maxOutputTokens: metadata.maxOutputTokens, requestID: metadata.requestID,
	}
	metadata.mu.Unlock()
	return snapshot
}

// SetAccessLogFirstEvent 写入首个上游语义事件耗时
func SetAccessLogFirstEvent(ctx context.Context, firstEvent time.Duration) {
	if metadata, ok := ctx.Value(accessLogContextKey{}).(*accessLogMetadata); ok {
		metadata.setFirstEvent(firstEvent)
	}
}

// SetAccessLogGenerationConfig 写入生成请求采用的参数
func SetAccessLogGenerationConfig(ctx context.Context, config aistudio.GenerationConfig) {
	if metadata, ok := ctx.Value(accessLogContextKey{}).(*accessLogMetadata); ok {
		metadata.setGenerationConfig(config)
	}
}

// SetAccessLogGenerationInput 写入生成请求输入摘要
func SetAccessLogGenerationInput(ctx context.Context, request aistudio.GenerateRequest) {
	if metadata, ok := ctx.Value(accessLogContextKey{}).(*accessLogMetadata); ok {
		metadata.setGenerationInput(request)
	}
}

// SetAccessLogUpstreamBytes 写入上游响应体字节数
func SetAccessLogUpstreamBytes(ctx context.Context, bytes int64) {
	if metadata, ok := ctx.Value(accessLogContextKey{}).(*accessLogMetadata); ok {
		metadata.setUpstreamBytes(bytes)
	}
}

// StartAccessLog 立即写入已经完成解析的请求开始记录
func StartAccessLog(ctx context.Context) {
	if metadata, ok := ctx.Value(accessLogContextKey{}).(*accessLogMetadata); ok {
		metadata.start(true)
	}
}

func formatLogFloat(value *float64) string {
	if value == nil {
		return "默认"
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}

func formatLogInt(value *int64) string {
	if value == nil {
		return "默认"
	}
	return strconv.FormatInt(*value, 10)
}

func formatLogThinking(config aistudio.GenerationConfig) string {
	if effort := strings.TrimSpace(config.ReasoningEffort); effort != "" {
		return effort
	}
	if config.ThinkingBudget != nil {
		return "预算" + strconv.FormatInt(*config.ThinkingBudget, 10)
	}
	return "默认"
}

// SetAccessLogTarget 写入请求实际使用的模型与账户
func SetAccessLogTarget(ctx context.Context, model string, account string) {
	if metadata, ok := ctx.Value(accessLogContextKey{}).(*accessLogMetadata); ok {
		metadata.setTarget(model, account)
	}
}

// SetAccessLogChannel 写入请求实际使用的上游通道
func SetAccessLogChannel(ctx context.Context, channel string) {
	if metadata, ok := ctx.Value(accessLogContextKey{}).(*accessLogMetadata); ok {
		metadata.mu.Lock()
		metadata.channel = strings.TrimSpace(channel)
		metadata.mu.Unlock()
	}
}

// SetAccessLogError 写入请求最终错误
func SetAccessLogError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	if metadata, ok := ctx.Value(accessLogContextKey{}).(*accessLogMetadata); ok {
		metadata.setRequestError(err)
	}
}

// SetAccessLogFinishReason 写入生成请求的上游终止原因
func SetAccessLogFinishReason(ctx context.Context, reason string) {
	if metadata, ok := ctx.Value(accessLogContextKey{}).(*accessLogMetadata); ok {
		metadata.setFinishReason(reason)
	}
}

// SetAccessLogGenerationResult 写入生成流的完成摘要
func SetAccessLogGenerationResult(
	ctx context.Context,
	usage *aistudio.Usage,
	toolCalls int,
) {
	if metadata, ok := ctx.Value(accessLogContextKey{}).(*accessLogMetadata); ok {
		metadata.setGenerationResult(usage, toolCalls)
	}
}

func requestLoggingMiddleware(config Config, next http.Handler) http.Handler {
	admin := config.Admin
	bodyLimit := config.RequestLogBodyLimit
	captureEnabled := bodyLimit > 0
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		metadata := &accessLogMetadata{admin: admin, method: r.Method, path: r.URL.Path, requestID: newID("req")}
		var requestCapture, responseCapture *captureBuffer
		if captureEnabled {
			requestCapture = newCaptureBuffer(bodyLimit)
			responseCapture = newCaptureBuffer(bodyLimit)
			r.Body = &teeReadCloser{source: r.Body, capture: requestCapture}
		}
		writer := &accessLogResponseWriter{ResponseWriter: w, metadata: metadata, capture: responseCapture}
		request := r.WithContext(context.WithValue(r.Context(), accessLogContextKey{}, metadata))
		next.ServeHTTP(writer, request)
		status := writer.status
		if status == 0 {
			status = http.StatusOK
		}
		snapshot := metadata.snapshot()
		if snapshot.canceled || errors.Is(r.Context().Err(), context.Canceled) {
			status = 499
		} else if status < http.StatusBadRequest && snapshot.failureStatus >= http.StatusBadRequest {
			status = snapshot.failureStatus
		}
		entry := AccessLog{
			Status: status, Latency: time.Since(started), FirstEvent: snapshot.firstEvent,
			UpstreamBytes: snapshot.upstreamBytes, Usage: snapshot.usage, ToolCalls: snapshot.toolCalls,
			InputMessages: snapshot.inputMessages, InputTextChars: snapshot.inputTextChars,
			InputMedia: snapshot.inputMedia, InputMediaBytes: snapshot.inputMediaBytes, InputFiles: snapshot.inputFiles,
			Temperature: snapshot.temperature, TopP: snapshot.topP,
			Thinking: snapshot.thinking, MaxOutputTokens: snapshot.maxOutputTokens,
			RequestID: snapshot.requestID,
			Method:    r.Method, Path: r.URL.Path, Model: snapshot.model, Account: snapshot.account, Channel: snapshot.channel,
			FinishReason: snapshot.finishReason, Error: snapshot.requestErr,
			Canceled: snapshot.canceled, Generation: snapshot.generation,
		}
		if config.RequestLog != nil || admin != nil {
			entry.RequestBody, entry.RequestBodyBytes, entry.RequestBodyTruncated = requestCapture.snapshot()
			entry.ResponseBody, entry.ResponseBodyBytes, entry.ResponseBodyTruncated = responseCapture.snapshot()
			entry.ClientIP = clientIP(r)
			entry.RemoteAddr = r.RemoteAddr
			entry.Query = sanitizeQuery(r.URL.RawQuery)
		}
		if config.RequestLog != nil {
			config.RequestLog.Record(entry)
		}
		if admin != nil {
			admin.RecordAccessLog(entry)
		}
	})
}

func setAccessLogResponseError(w http.ResponseWriter, message string) {
	if writer, ok := w.(interface{ setError(string) }); ok {
		writer.setError(message)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key, X-Goog-API-Key, Anthropic-Version, Anthropic-Beta")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// controlPlaneMiddleware 放行本机回环访问，并在启用远程访问时校验管理密钥
func controlPlaneMiddleware(adminKey string, remoteAccess bool, next http.Handler) http.Handler {
	adminKey = strings.TrimSpace(adminKey)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if loopbackRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !remoteAccess {
			writeAdminError(w, http.StatusForbidden, "control_plane_forbidden", "Control plane is only available from loopback")
			return
		}
		if adminKey == "" {
			writeAdminError(w, http.StatusForbidden, "admin_key_required", "Remote control plane access requires ADMIN_API_KEY")
			return
		}
		if subtle.ConstantTimeCompare([]byte(requestAdminKey(r)), []byte(adminKey)) != 1 {
			writeAuthError(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackRequest 判断请求是否同时来自回环地址且访问回环主机名
func loopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() || !loopbackHost(r.Host) {
		return false
	}
	return true
}

// requestAdminKey 从请求头或查询参数读取管理密钥
func requestAdminKey(r *http.Request) string {
	if key := strings.TrimSpace(r.Header.Get("X-Admin-Key")); key != "" {
		return key
	}
	return strings.TrimSpace(r.URL.Query().Get("key"))
}

// sensitiveQueryKeys 列出需要脱敏的查询参数名
var sensitiveQueryKeys = map[string]struct{}{
	"key": {}, "api_key": {}, "apikey": {}, "token": {}, "access_token": {},
}

// sanitizeQuery 对密钥类查询参数做脱敏并保留其余参数
func sanitizeQuery(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	for index, part := range parts {
		name, _, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		if _, sensitive := sensitiveQueryKeys[strings.ToLower(name)]; sensitive {
			parts[index] = name + "=***"
		}
	}
	return strings.Join(parts, "&")
}

// clientIP 优先读取 X-Forwarded-For 首个地址，否则回退直连地址
func clientIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		if first, _, found := strings.Cut(forwarded, ","); found {
			forwarded = strings.TrimSpace(first)
		}
		if forwarded != "" {
			return forwarded
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// captureBuffer 记录正文前缀与总字节数，nil 值安全
type captureBuffer struct {
	limit     int
	buf       []byte
	total     int64
	truncated bool
}

func newCaptureBuffer(limit int) *captureBuffer {
	return &captureBuffer{limit: limit}
}

// append 追加正文内容，超过上限后只统计总字节数
func (capture *captureBuffer) append(data []byte) {
	if capture == nil {
		return
	}
	capture.total += int64(len(data))
	remaining := capture.limit - len(capture.buf)
	if remaining <= 0 {
		capture.truncated = true
		return
	}
	if len(data) > remaining {
		capture.buf = append(capture.buf, data[:remaining]...)
		capture.truncated = true
		return
	}
	capture.buf = append(capture.buf, data...)
}

// snapshot 返回正文前缀、总字节数与截断标记
func (capture *captureBuffer) snapshot() (string, int64, bool) {
	if capture == nil {
		return "", 0, false
	}
	return string(capture.buf), capture.total, capture.truncated
}

// teeReadCloser 在读取请求体时同步捕获前缀
type teeReadCloser struct {
	source  io.ReadCloser
	capture *captureBuffer
}

func (reader *teeReadCloser) Read(data []byte) (int, error) {
	n, err := reader.source.Read(data)
	if n > 0 {
		reader.capture.append(data[:n])
	}
	return n, err
}

func (reader *teeReadCloser) Close() error {
	return reader.source.Close()
}

// loopbackHost 判断 Host 或 Origin 主机名是否为 localhost 或回环地址
func loopbackHost(host string) bool {
	name := host
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		name = hostname
	}
	name = strings.Trim(name, "[]")
	if strings.EqualFold(name, "localhost") {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

// browserOriginMiddleware 在未配置 API key 时拒绝外部网页与 null 来源的浏览器请求
func browserOriginMiddleware(requiredKey string, next http.Handler) http.Handler {
	if strings.TrimSpace(requiredKey) != "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originValue := strings.TrimSpace(r.Header.Get("Origin"))
		if originValue == "" {
			next.ServeHTTP(w, r)
			return
		}
		origin, err := url.Parse(originValue)
		webOrigin := err != nil || originValue == "null" || origin.Scheme == "http" || origin.Scheme == "https"
		if webOrigin && (err != nil || !loopbackHost(origin.Host)) {
			writeAuthError(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// maxPublicBodyBytes 为公开接口请求体上限，可容纳 Base64 编码后的最大文件
const maxPublicBodyBytes = openAIFileMaxBytes/3*4 + openAIFileRequestOverhead

// bodyLimitMiddleware 限制公开接口请求体大小
func bodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxPublicBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func sameOriginMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originValue := strings.TrimSpace(r.Header.Get("Origin"))
		if originValue == "" {
			next.ServeHTTP(w, r)
			return
		}
		origin, err := url.Parse(originValue)
		if err != nil || origin.Host == "" || !strings.EqualFold(origin.Host, r.Host) {
			writeAdminError(w, http.StatusForbidden, "control_plane_origin_forbidden", "Control plane requires a same-origin browser request")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func authMiddleware(requiredKey string, next http.Handler) http.Handler {
	requiredKey = strings.TrimSpace(requiredKey)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requiredKey == "" {
			next.ServeHTTP(w, r)
			return
		}
		provided := requestAPIKey(r)
		if subtle.ConstantTimeCompare([]byte(provided), []byte(requiredKey)) != 1 {
			writeAuthError(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestAPIKey(r *http.Request) string {
	if key := strings.TrimSpace(r.URL.Query().Get("key")); key != "" {
		return key
	}
	if key := strings.TrimSpace(r.Header.Get("X-Goog-API-Key")); key != "" {
		return key
	}
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		return key
	}
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	scheme, key, ok := strings.Cut(authorization, " ")
	if ok && strings.EqualFold(scheme, "Bearer") {
		return strings.TrimSpace(key)
	}
	return ""
}
