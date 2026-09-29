// Package proxybridge 在本机提供无认证 HTTP 代理，把流量转发到带认证的上游代理
package proxybridge

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

const (
	handshakeTimeout = 30 * time.Second
)

// Bridge 本机代理中转实例，浏览器连接本机无认证端口，由中转完成上游认证
type Bridge struct {
	upstream *url.URL
	listener net.Listener
	forward  *http.Transport
	socks    proxy.ContextDialer

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// Start 为带认证的上游代理启动本机中转；上游没有认证信息时返回 nil 表示无需中转
func Start(upstreamURL string) (*Bridge, error) {
	upstreamURL = strings.TrimSpace(upstreamURL)
	if upstreamURL == "" {
		return nil, nil
	}
	parsed, err := url.Parse(upstreamURL)
	if err != nil || parsed.Hostname() == "" {
		return nil, fmt.Errorf("代理 URL 无效")
	}
	if parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
		return nil, nil
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5":
	default:
		return nil, fmt.Errorf("代理协议必须是 http、https 或 socks5")
	}
	target, err := hostPort(parsed)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("创建本机代理中转: %w", err)
	}
	bridge := &Bridge{upstream: parsed, listener: listener, conns: make(map[net.Conn]struct{})}
	if strings.EqualFold(parsed.Scheme, "socks5") {
		password, _ := parsed.User.Password()
		dialer, err := proxy.SOCKS5("tcp", target, &proxy.Auth{User: parsed.User.Username(), Password: password}, &net.Dialer{Timeout: handshakeTimeout})
		if err != nil {
			_ = listener.Close()
			return nil, fmt.Errorf("创建 SOCKS5 上游连接: %w", err)
		}
		contextDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			_ = listener.Close()
			return nil, fmt.Errorf("SOCKS5 上游不支持上下文取消")
		}
		bridge.socks = contextDialer
		bridge.forward = &http.Transport{
			DialContext: contextDialer.DialContext, DisableKeepAlives: true,
			ForceAttemptHTTP2: false,
		}
	} else {
		bridge.forward = &http.Transport{
			Proxy:             http.ProxyURL(parsed),
			DialContext:       (&net.Dialer{Timeout: handshakeTimeout, KeepAlive: 30 * time.Second}).DialContext,
			DisableKeepAlives: true,
			ForceAttemptHTTP2: false,
		}
	}
	go bridge.serve()
	return bridge, nil
}

// URL 返回浏览器使用的本机无认证代理地址
func (bridge *Bridge) URL() string {
	if bridge == nil || bridge.listener == nil {
		return ""
	}
	return "http://" + bridge.listener.Addr().String()
}

// Close 关闭监听并终止全部活动连接，可安全重复调用
func (bridge *Bridge) Close() error {
	if bridge == nil {
		return nil
	}
	bridge.mu.Lock()
	if bridge.closed {
		bridge.mu.Unlock()
		return nil
	}
	bridge.closed = true
	conns := make([]net.Conn, 0, len(bridge.conns))
	for conn := range bridge.conns {
		conns = append(conns, conn)
	}
	bridge.mu.Unlock()
	err := bridge.listener.Close()
	for _, conn := range conns {
		_ = conn.Close()
	}
	if bridge.forward != nil {
		bridge.forward.CloseIdleConnections()
	}
	return err
}

// serve 接受本机连接并逐个处理
func (bridge *Bridge) serve() {
	for {
		conn, err := bridge.listener.Accept()
		if err != nil {
			return
		}
		bridge.mu.Lock()
		if bridge.closed {
			bridge.mu.Unlock()
			_ = conn.Close()
			return
		}
		bridge.conns[conn] = struct{}{}
		bridge.mu.Unlock()
		go func() {
			defer func() {
				bridge.mu.Lock()
				delete(bridge.conns, conn)
				bridge.mu.Unlock()
				_ = conn.Close()
			}()
			bridge.handle(conn)
		}()
	}
}

// handle 读取一条代理请求并分流到 CONNECT 隧道或普通转发
func (bridge *Bridge) handle(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	request, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}
	if request.Method == http.MethodConnect {
		bridge.handleConnect(conn, request)
		return
	}
	bridge.handleForward(conn, request)
}

// handleConnect 处理浏览器发向本机的 CONNECT 隧道
func (bridge *Bridge) handleConnect(conn net.Conn, request *http.Request) {
	target := request.Host
	if target == "" {
		target = request.URL.Host
	}
	if target == "" {
		writeSimpleResponse(conn, http.StatusBadRequest, "缺少 CONNECT 目标")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()
	var upstream net.Conn
	var err error
	if bridge.socks != nil {
		upstream, err = bridge.socks.DialContext(ctx, "tcp", target)
	} else {
		upstream, err = bridge.dialHTTPProxyTunnel(ctx, target)
	}
	if err != nil {
		writeSimpleResponse(conn, http.StatusBadGateway, "上游代理连接失败")
		return
	}
	defer upstream.Close()
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	relay(conn, upstream)
}

// dialHTTPProxyTunnel 通过 http/https 上游代理建立 CONNECT 隧道
func (bridge *Bridge) dialHTTPProxyTunnel(ctx context.Context, target string) (net.Conn, error) {
	proxyTarget, err := hostPort(bridge.upstream)
	if err != nil {
		return nil, err
	}
	var connection net.Conn
	if strings.EqualFold(bridge.upstream.Scheme, "https") {
		dialer := tls.Dialer{
			NetDialer: &net.Dialer{Timeout: handshakeTimeout},
			Config: &tls.Config{
				ServerName: bridge.upstream.Hostname(),
				NextProtos: []string{"http/1.1"},
			},
		}
		connection, err = dialer.DialContext(ctx, "tcp", proxyTarget)
	} else {
		connection, err = (&net.Dialer{Timeout: handshakeTimeout}).DialContext(ctx, "tcp", proxyTarget)
	}
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = connection.Close()
		}
	}()
	request := (&http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Host: target},
		Host:   target,
		Header: make(http.Header),
	}).WithContext(ctx)
	password, _ := bridge.upstream.User.Password()
	credentials := bridge.upstream.User.Username() + ":" + password
	request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
	if err := request.Write(connection); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, fmt.Errorf("上游代理 CONNECT 返回 %s", response.Status)
	}
	success = true
	return &bufferedConn{Conn: connection, reader: reader}, nil
}

// handleForward 处理普通 HTTP 请求：经上游代理转发并写回响应
func (bridge *Bridge) handleForward(conn net.Conn, request *http.Request) {
	request.RequestURI = ""
	request.Header.Del("Proxy-Authorization")
	request.Header.Del("Proxy-Connection")
	response, err := bridge.forward.RoundTrip(request)
	if err != nil {
		writeSimpleResponse(conn, http.StatusBadGateway, "上游代理请求失败")
		return
	}
	defer response.Body.Close()
	response.Close = true
	_ = response.Write(conn)
}

// relay 双向复制两个连接，任一方向结束后通知对端
func relay(left, right net.Conn) {
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		_, _ = io.Copy(left, right)
		closeWrite(left)
	}()
	go func() {
		defer wait.Done()
		_, _ = io.Copy(right, left)
		closeWrite(right)
	}()
	wait.Wait()
}

func closeWrite(conn net.Conn) {
	if closer, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
	}
}

func hostPort(value *url.URL) (string, error) {
	if value.Port() != "" {
		return value.Host, nil
	}
	switch strings.ToLower(value.Scheme) {
	case "http":
		return net.JoinHostPort(value.Hostname(), "80"), nil
	case "https":
		return net.JoinHostPort(value.Hostname(), "443"), nil
	}
	return "", fmt.Errorf("代理 URL 缺少端口")
}

func writeSimpleResponse(conn net.Conn, status int, message string) {
	body := message + "\n"
	response := fmt.Sprintf(
		"HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(body), body,
	)
	_, _ = io.WriteString(conn, response)
}

// bufferedConn 在已缓冲的读取器之上继续读取连接
type bufferedConn struct {
	net.Conn
	reader io.Reader
}

func (conn *bufferedConn) Read(data []byte) (int, error) {
	return conn.reader.Read(data)
}

func (conn *bufferedConn) CloseWrite() error {
	if closer, ok := conn.Conn.(interface{ CloseWrite() error }); ok {
		return closer.CloseWrite()
	}
	return nil
}
