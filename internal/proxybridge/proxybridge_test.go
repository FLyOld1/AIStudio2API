package proxybridge

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// echoServer 启动回显 TCP 服务
func echoServer(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动回显服务: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener
}

// requireProxyAuth 校验 Proxy-Authorization Basic 凭据
func requireProxyAuth(request *http.Request, user, pass string) bool {
	header := request.Header.Get("Proxy-Authorization")
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(decoded, []byte(user+":"+pass)) == 1
}

// newAuthHTTPProxy 启动要求 Basic 认证的 HTTP 代理
func newAuthHTTPProxy(t *testing.T, user, pass string) *httptest.Server {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireProxyAuth(r, user, pass) {
			w.Header().Set("Proxy-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		if r.Method == http.MethodConnect {
			target := r.Host
			if target == "" {
				target = r.URL.Host
			}
			upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
			if err != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			conn, buffered, err := hijacker.Hijack()
			if err != nil {
				_ = upstream.Close()
				return
			}
			_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
			_ = buffered.Flush()
			go func() {
				defer upstream.Close()
				_, _ = io.Copy(upstream, buffered)
			}()
			_, _ = io.Copy(conn, upstream)
			_ = conn.Close()
			return
		}
		outbound := r.Clone(context.Background())
		outbound.RequestURI = ""
		response, err := http.DefaultTransport.RoundTrip(outbound)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for name, values := range response.Header {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// newAuthSOCKS5Server 启动要求用户名密码认证的最小 SOCKS5 服务，仅支持 CONNECT
func newAuthSOCKS5Server(t *testing.T, user, pass string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动 SOCKS5 服务: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveSOCKS5(conn, user, pass)
		}
	}()
	return listener.Addr().String()
}

func serveSOCKS5(conn net.Conn, user, pass string) {
	defer conn.Close()
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		return
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x02}); err != nil {
		return
	}
	authHead := make([]byte, 2)
	if _, err := io.ReadFull(conn, authHead); err != nil {
		return
	}
	username := make([]byte, int(authHead[1]))
	if _, err := io.ReadFull(conn, username); err != nil {
		return
	}
	length := make([]byte, 1)
	if _, err := io.ReadFull(conn, length); err != nil {
		return
	}
	password := make([]byte, int(length[0]))
	if _, err := io.ReadFull(conn, password); err != nil {
		return
	}
	if string(username) != user || string(password) != pass {
		_, _ = conn.Write([]byte{0x01, 0x01})
		return
	}
	if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
		return
	}
	header := make([]byte, 3)
	if _, err := io.ReadFull(conn, header); err != nil || header[1] != 0x01 {
		return
	}
	atyp := make([]byte, 1)
	if _, err := io.ReadFull(conn, atyp); err != nil {
		return
	}
	var host string
	switch atyp[0] {
	case 0x01:
		raw := make([]byte, 4)
		if _, err := io.ReadFull(conn, raw); err != nil {
			return
		}
		host = net.IP(raw).String()
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return
		}
		raw := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, raw); err != nil {
			return
		}
		host = string(raw)
	case 0x04:
		raw := make([]byte, 16)
		if _, err := io.ReadFull(conn, raw); err != nil {
			return
		}
		host = net.IP(raw).String()
	default:
		return
	}
	portRaw := make([]byte, 2)
	if _, err := io.ReadFull(conn, portRaw); err != nil {
		return
	}
	port := int(portRaw[0])<<8 | int(portRaw[1])
	target, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer target.Close()
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	go func() { _, _ = io.Copy(target, conn) }()
	_, _ = io.Copy(conn, target)
}

// bridgeAddress 解析 bridge 的本机地址
func bridgeAddress(t *testing.T, bridge *Bridge) string {
	t.Helper()
	parsed, err := url.Parse(bridge.URL())
	if err != nil {
		t.Fatalf("解析 bridge 地址: %v", err)
	}
	return parsed.Host
}

// proxyConnect 经 bridge 建立 CONNECT 隧道
func proxyConnect(t *testing.T, bridge *Bridge, target string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", bridgeAddress(t, bridge), 5*time.Second)
	if err != nil {
		t.Fatalf("连接 bridge: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("读取 CONNECT 响应: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT 状态 %d", response.StatusCode)
	}
	return conn
}

func TestStartSkipsUpstreamWithoutCredentials(t *testing.T) {
	for _, upstream := range []string{"", "http://127.0.0.1:8080", "socks5://127.0.0.1:1080"} {
		bridge, err := Start(upstream)
		if err != nil {
			t.Fatalf("Start(%q): %v", upstream, err)
		}
		if bridge != nil {
			t.Fatalf("无认证上游不应创建中转: %q", upstream)
		}
	}
}

func TestBridgeHTTPUpstreamConnectTunnel(t *testing.T) {
	echo := echoServer(t)
	proxied := newAuthHTTPProxy(t, "user", "pass")
	bridge, err := Start("http://user:pass@" + strings.TrimPrefix(proxied.URL, "http://"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer bridge.Close()
	conn := proxyConnect(t, bridge, echo.Addr().String())
	_, _ = conn.Write([]byte("ping"))
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatalf("读取回显: %v", err)
	}
	if string(buffer) != "ping" {
		t.Fatalf("回显不符: %q", buffer)
	}
}

func TestBridgeHTTPUpstreamRejectsWrongCredentials(t *testing.T) {
	echo := echoServer(t)
	proxied := newAuthHTTPProxy(t, "user", "pass")
	bridge, err := Start("http://user:wrong@" + strings.TrimPrefix(proxied.URL, "http://"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer bridge.Close()
	conn, err := net.DialTimeout("tcp", bridgeAddress(t, bridge), 5*time.Second)
	if err != nil {
		t.Fatalf("连接 bridge: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	target := echo.Addr().String()
	_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("读取 CONNECT 响应: %v", err)
	}
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("凭据错误应返回 502，实际 %d", response.StatusCode)
	}
}

func TestBridgeHTTPUpstreamForwardRequest(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pong"))
	}))
	defer target.Close()
	proxied := newAuthHTTPProxy(t, "user", "pass")
	bridge, err := Start("http://user:pass@" + strings.TrimPrefix(proxied.URL, "http://"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer bridge.Close()

	conn, err := net.DialTimeout("tcp", bridgeAddress(t, bridge), 5*time.Second)
	if err != nil {
		t.Fatalf("连接 bridge: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = fmt.Fprintf(conn, "GET %s/ HTTP/1.1\r\nHost: %s\r\n\r\n", target.URL, strings.TrimPrefix(target.URL, "http://"))
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("读取响应: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取响应体: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "pong" {
		t.Fatalf("转发结果不符: %d %q", response.StatusCode, body)
	}
}

func TestBridgeSOCKS5UpstreamConnectTunnel(t *testing.T) {
	echo := echoServer(t)
	socksAddr := newAuthSOCKS5Server(t, "user", "pass")
	bridge, err := Start("socks5://user:pass@" + socksAddr)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer bridge.Close()
	conn := proxyConnect(t, bridge, echo.Addr().String())
	_, _ = conn.Write([]byte("ping"))
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatalf("读取回显: %v", err)
	}
	if string(buffer) != "ping" {
		t.Fatalf("回显不符: %q", buffer)
	}
}

func TestBridgeSOCKS5UpstreamRejectsWrongCredentials(t *testing.T) {
	echo := echoServer(t)
	socksAddr := newAuthSOCKS5Server(t, "user", "pass")
	bridge, err := Start("socks5://user:wrong@" + socksAddr)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer bridge.Close()
	conn, err := net.DialTimeout("tcp", bridgeAddress(t, bridge), 5*time.Second)
	if err != nil {
		t.Fatalf("连接 bridge: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	target := echo.Addr().String()
	_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("读取 CONNECT 响应: %v", err)
	}
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("凭据错误应返回 502，实际 %d", response.StatusCode)
	}
}
