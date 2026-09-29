package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// recordingSink 记录落盘接口收到的访问记录
type recordingSink struct {
	entries []AccessLog
}

func (sink *recordingSink) Record(entry AccessLog) {
	sink.entries = append(sink.entries, entry)
}

// okHandler 返回固定 200 响应
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// controlPlaneRequest 构造带指定来源与主机名的管理接口请求
func controlPlaneRequest(target, remoteAddr, host string, headers map[string]string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.RemoteAddr = remoteAddr
	request.Host = host
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return request
}

func TestControlPlaneLoopbackRequiresNoKey(t *testing.T) {
	handler := controlPlaneMiddleware("", false, okHandler())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, controlPlaneRequest("http://127.0.0.1:2048/api/status", "127.0.0.1:5555", "127.0.0.1:2048", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("回环访问应放行，实际状态 %d", recorder.Code)
	}
}

func TestControlPlaneRemoteDeniedWhenDisabled(t *testing.T) {
	handler := controlPlaneMiddleware("secret", false, okHandler())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, controlPlaneRequest("http://api.example.com/api/status", "203.0.113.5:5555", "api.example.com", map[string]string{"X-Admin-Key": "secret"}))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("未开启远程访问应拒绝，实际状态 %d", recorder.Code)
	}
}

func TestControlPlaneRemoteRequiresKey(t *testing.T) {
	handler := controlPlaneMiddleware("secret", true, okHandler())
	request := func(headers map[string]string, target string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, controlPlaneRequest(target, "203.0.113.5:5555", "api.example.com", headers))
		return recorder
	}

	if code := request(nil, "http://api.example.com/api/status").Code; code != http.StatusUnauthorized {
		t.Fatalf("缺少密钥应返回 401，实际 %d", code)
	}
	if code := request(map[string]string{"X-Admin-Key": "wrong"}, "http://api.example.com/api/status").Code; code != http.StatusUnauthorized {
		t.Fatalf("密钥错误应返回 401，实际 %d", code)
	}
	if code := request(map[string]string{"X-Admin-Key": "secret"}, "http://api.example.com/api/status").Code; code != http.StatusOK {
		t.Fatalf("请求头密钥正确应放行，实际 %d", code)
	}
	if code := request(nil, "http://api.example.com/api/events?key=secret").Code; code != http.StatusOK {
		t.Fatalf("查询参数密钥正确应放行，实际 %d", code)
	}
}

func TestControlPlaneRemoteAccessWithoutConfiguredKey(t *testing.T) {
	handler := controlPlaneMiddleware("", true, okHandler())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, controlPlaneRequest("http://api.example.com/api/status", "203.0.113.5:5555", "api.example.com", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("未配置管理密钥时应拒绝远程访问，实际状态 %d", recorder.Code)
	}
}

func TestControlPlaneProxyHostRequiresKey(t *testing.T) {
	// 反向代理场景：直连地址为回环但 Host 是域名，必须携带密钥
	handler := controlPlaneMiddleware("secret", true, okHandler())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, controlPlaneRequest("http://api.example.com/api/status", "127.0.0.1:5555", "api.example.com", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("Host 非回环时应要求密钥，实际状态 %d", recorder.Code)
	}
}

func TestSanitizeQueryRedactsSecrets(t *testing.T) {
	got := sanitizeQuery("a=1&key=secret&api_key=x&token=y&b=2")
	want := "a=1&key=***&api_key=***&token=***&b=2"
	if got != want {
		t.Fatalf("查询参数脱敏不符: got %q want %q", got, want)
	}
	if sanitizeQuery("") != "" {
		t.Fatalf("空查询串应返回空")
	}
}

func TestClientIPPrefersForwardedFor(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:2048/v1/models", nil)
	request.RemoteAddr = "127.0.0.1:5555"
	if got := clientIP(request); got != "127.0.0.1" {
		t.Fatalf("无转发头应回退直连地址，实际 %q", got)
	}
	request.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if got := clientIP(request); got != "1.2.3.4" {
		t.Fatalf("应取转发头首个地址，实际 %q", got)
	}
}

func TestRequestLoggingCapturesBodyAndTruncates(t *testing.T) {
	sink := &recordingSink{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Errorf("读取请求体: %v", err)
		}
		_, _ = w.Write([]byte("abcdef"))
	})
	handler := requestLoggingMiddleware(Config{RequestLog: sink, RequestLogBodyLimit: 4}, next)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:2048/v1/chat/completions?key=secret", bytes.NewBufferString("123456"))
	handler.ServeHTTP(recorder, request)

	if len(sink.entries) != 1 {
		t.Fatalf("期望 1 条落盘记录，实际 %d", len(sink.entries))
	}
	entry := sink.entries[0]
	if entry.RequestBody != "1234" || entry.RequestBodyBytes != 6 || !entry.RequestBodyTruncated {
		t.Fatalf("请求体捕获不符: %+v", entry)
	}
	if entry.ResponseBody != "abcd" || entry.ResponseBodyBytes != 6 || !entry.ResponseBodyTruncated {
		t.Fatalf("响应体捕获不符: %+v", entry)
	}
	if entry.Query != "key=***" {
		t.Fatalf("查询参数应脱敏，实际 %q", entry.Query)
	}
	if entry.ClientIP != "192.0.2.1" {
		t.Fatalf("客户端地址不符: %q", entry.ClientIP)
	}
}

func TestRequestLoggingAggregatesStreamingResponse(t *testing.T) {
	sink := &recordingSink{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello "))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush: %v", err)
		}
		_, _ = w.Write([]byte("world"))
	})
	handler := requestLoggingMiddleware(Config{RequestLog: sink, RequestLogBodyLimit: 8}, next)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "http://127.0.0.1:2048/v1/chat/completions", nil))

	if len(sink.entries) != 1 {
		t.Fatalf("期望 1 条落盘记录，实际 %d", len(sink.entries))
	}
	entry := sink.entries[0]
	if entry.ResponseBody != "hello wo" || entry.ResponseBodyBytes != 11 || !entry.ResponseBodyTruncated {
		t.Fatalf("流式响应聚合不符: %+v", entry)
	}
}

func TestRequestLoggingDisabledKeepsBodyReadable(t *testing.T) {
	var received string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读取请求体: %v", err)
		}
		received = string(data)
		_, _ = w.Write([]byte("ok"))
	})
	handler := requestLoggingMiddleware(Config{RequestLogBodyLimit: 4}, next)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:2048/v1/chat/completions", bytes.NewBufferString("123456"))
	handler.ServeHTTP(recorder, request)

	if received != "123456" {
		t.Fatalf("关闭落盘时请求体应保持完整，实际 %q", received)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("响应状态不符: %d", recorder.Code)
	}
}
