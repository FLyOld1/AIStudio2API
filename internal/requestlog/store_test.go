package requestlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readEntries 读取目录内全部 JSONL 条目
func readEntries(t *testing.T, dir string) []Entry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	var result []Entry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("读取文件失败: %v", err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var item Entry
			if err := json.Unmarshal([]byte(line), &item); err != nil {
				t.Fatalf("解析条目失败: %v\n%s", err, line)
			}
			result = append(result, item)
		}
	}
	return result
}

// logFileNames 返回目录内的请求日志文件名
func logFileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasPrefix(name, filePrefix) && strings.HasSuffix(name, fileSuffix) {
			names = append(names, name)
		}
	}
	return names
}

// openTestStore 创建测试用存储
func openTestStore(t *testing.T, dir string, maxFileBytes int64) *Store {
	t.Helper()
	store, err := Open(Options{
		Dir: dir, MaxFileBytes: maxFileBytes, MaxTotalBytes: 8 << 20, Retention: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

func TestRecordWritesJSONLine(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir, 1<<20)
	store.Record(Entry{Time: time.Now(), RequestID: "req_1", Method: "POST", Path: "/v1/chat/completions", Status: 200, RequestBody: `{"a":1}`})
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	entries := readEntries(t, dir)
	if len(entries) != 1 {
		t.Fatalf("期望 1 条记录，实际 %d", len(entries))
	}
	if entries[0].RequestID != "req_1" || entries[0].RequestBody != `{"a":1}` {
		t.Fatalf("记录内容不符: %+v", entries[0])
	}
	if entries[0].Time.IsZero() {
		t.Fatalf("时间字段应被序列化")
	}
}

func TestRotationByFileSize(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir, 400)
	for i := 0; i < 5; i++ {
		store.Record(Entry{RequestID: fmt.Sprintf("req_%d", i), ResponseBody: strings.Repeat("x", 100)})
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if files := logFileNames(t, dir); len(files) < 2 {
		t.Fatalf("期望按大小轮转出多个文件，实际 %d", len(files))
	}
	if entries := readEntries(t, dir); len(entries) != 5 {
		t.Fatalf("期望 5 条记录，实际 %d", len(entries))
	}
}

func TestOversizedEntryGetsOwnFile(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir, 128)
	store.Record(Entry{RequestID: "big", ResponseBody: strings.Repeat("y", 1024)})
	store.Record(Entry{RequestID: "small"})
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if files := logFileNames(t, dir); len(files) != 2 {
		t.Fatalf("超大记录应独占文件，实际 %d 个文件", len(files))
	}
	if entries := readEntries(t, dir); len(entries) != 2 {
		t.Fatalf("期望 2 条记录，实际 %d", len(entries))
	}
}

func TestPruneRemovesExpiredByMtime(t *testing.T) {
	dir := t.TempDir()
	expired := filepath.Join(dir, filePrefix+"expired"+fileSuffix)
	fresh := filepath.Join(dir, filePrefix+"fresh"+fileSuffix)
	for _, path := range []string{expired, fresh} {
		if err := os.WriteFile(path, []byte(strings.Repeat("a", 64)), 0o600); err != nil {
			t.Fatalf("写入测试文件: %v", err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(expired, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	store := openTestStore(t, dir, 1<<20)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("过期文件应被删除")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("未过期文件不应被删除: %v", err)
	}
}

func TestPruneOverTotalSizeDeletesOldest(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, filePrefix+"older"+fileSuffix)
	newer := filepath.Join(dir, filePrefix+"newer"+fileSuffix)
	for _, path := range []string{older, newer} {
		if err := os.WriteFile(path, []byte(strings.Repeat("b", 100)), 0o600); err != nil {
			t.Fatalf("写入测试文件: %v", err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	store, err := Open(Options{Dir: dir, MaxFileBytes: 100, MaxTotalBytes: 150, Retention: 24 * time.Hour})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(older); !os.IsNotExist(err) {
		t.Fatalf("超出总量时应先删除最旧文件")
	}
	if _, err := os.Stat(newer); err != nil {
		t.Fatalf("最新文件不应被删除: %v", err)
	}
}

func TestClearRemovesFilesAndReopens(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir, 1<<20)
	store.Record(Entry{RequestID: "before"})
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if files := logFileNames(t, dir); len(files) != 0 {
		t.Fatalf("Clear 后不应存在日志文件: %v", files)
	}
	store.Record(Entry{RequestID: "after"})
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	entries := readEntries(t, dir)
	if len(entries) != 1 || entries[0].RequestID != "after" {
		t.Fatalf("Clear 后应能继续写入新文件: %+v", entries)
	}
}
