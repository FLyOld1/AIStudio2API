package app

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDisplayBodyTruncatesAndKeepsValidUTF8(t *testing.T) {
	if body, truncated := displayBody("hello", false); body != "hello" || truncated {
		t.Fatalf("短正文不应截断: %q %v", body, truncated)
	}
	if body, truncated := displayBody("x", true); body != "x" || !truncated {
		t.Fatalf("已有截断标记应保留: %q %v", body, truncated)
	}
	long := strings.Repeat("a", requestLogDisplayLimit+10)
	body, truncated := displayBody(long, false)
	if !truncated || len(body) != requestLogDisplayLimit {
		t.Fatalf("超长正文应截断到上限: len=%d truncated=%v", len(body), truncated)
	}
	multibyte := strings.Repeat("中", requestLogDisplayLimit)
	clipped, truncated := displayBody(multibyte, false)
	if !truncated || !utf8.ValidString(clipped) {
		t.Fatalf("截断后必须是合法 UTF-8: truncated=%v", truncated)
	}
}
