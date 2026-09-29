package requestlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultQueueSize = 256
	pruneInterval    = 10 * time.Minute
	dropWarnEvery    = 64
	writeWarnEvery   = 64
	filePrefix       = "requests-"
	fileSuffix       = ".jsonl"
	fileTimeLayout   = "20060102T150405.000"
)

// Options 定义请求日志存储参数
type Options struct {
	Dir           string
	MaxFileBytes  int64
	MaxTotalBytes int64
	Retention     time.Duration
	QueueSize     int
}

// Store 是请求日志的 JSONL 文件存储，由单个 goroutine 串行写入与清理
type Store struct {
	dir           string
	maxFileBytes  int64
	maxTotalBytes int64
	retention     time.Duration

	entries   chan Entry
	clear     chan chan error
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once

	sequence atomic.Uint64
	dropped  atomic.Int64
	writeErr atomic.Int64

	current      *os.File
	currentPath  string
	currentBytes int64
}

// Open 创建目录、执行一次清理并启动写入 goroutine
func Open(options Options) (*Store, error) {
	dir := strings.TrimSpace(options.Dir)
	if dir == "" {
		return nil, fmt.Errorf("请求日志目录不能为空")
	}
	if options.MaxFileBytes <= 0 || options.MaxTotalBytes <= 0 || options.Retention <= 0 {
		return nil, fmt.Errorf("请求日志参数必须为正数")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建请求日志目录: %w", err)
	}
	queueSize := options.QueueSize
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	store := &Store{
		dir:           dir,
		maxFileBytes:  options.MaxFileBytes,
		maxTotalBytes: options.MaxTotalBytes,
		retention:     options.Retention,
		entries:       make(chan Entry, queueSize),
		clear:         make(chan chan error),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
	store.prune()
	go store.run()
	return store, nil
}

// Record 非阻塞提交一条请求日志，队列满时丢弃并限频告警
func (store *Store) Record(entry Entry) {
	select {
	case store.entries <- entry:
	default:
		if dropped := store.dropped.Add(1); dropped%dropWarnEvery == 1 {
			slog.Warn("请求日志队列已满，丢弃记录", "dropped", dropped)
		}
	}
}

// Clear 同步清空目录内全部请求日志文件，后续写入自动创建新文件
func (store *Store) Clear() error {
	reply := make(chan error, 1)
	select {
	case store.clear <- reply:
	case <-store.done:
		return fmt.Errorf("请求日志已关闭")
	}
	select {
	case err := <-reply:
		return err
	case <-store.done:
		return fmt.Errorf("请求日志已关闭")
	}
}

// Close 停止写入 goroutine 并落盘剩余记录，可安全重复调用
func (store *Store) Close() error {
	store.closeOnce.Do(func() { close(store.stop) })
	<-store.done
	return nil
}

// run 是唯一的写入与清理 goroutine
func (store *Store) run() {
	defer close(store.done)
	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	for {
		select {
		case entry := <-store.entries:
			store.writeEntry(entry)
		case reply := <-store.clear:
			reply <- store.handleClear()
		case <-ticker.C:
			store.prune()
		case <-store.stop:
			for {
				select {
				case entry := <-store.entries:
					store.writeEntry(entry)
				default:
					if store.current != nil {
						if err := store.current.Close(); err != nil {
							slog.Warn("关闭请求日志文件失败", "error", err)
						}
						store.current = nil
					}
					return
				}
			}
		}
	}
}

// writeEntry 序列化并写入一条记录，按文件大小轮转
func (store *Store) writeEntry(entry Entry) {
	line, err := json.Marshal(entry)
	if err != nil {
		slog.Warn("序列化请求日志失败", "error", err)
		return
	}
	if store.current == nil {
		if err := store.rotate(); err != nil {
			store.warnWriteError(err)
			return
		}
	}
	if store.currentBytes > 0 && store.currentBytes+int64(len(line))+1 > store.maxFileBytes {
		if err := store.rotate(); err != nil {
			store.warnWriteError(err)
			return
		}
	}
	written, err := store.current.Write(append(line, '\n'))
	if err != nil {
		store.warnWriteError(err)
		return
	}
	store.currentBytes += int64(written)
}

// rotate 关闭当前文件并创建新文件，同时执行一次清理
func (store *Store) rotate() error {
	if store.current != nil {
		if err := store.current.Close(); err != nil {
			slog.Warn("关闭请求日志文件失败", "error", err)
		}
		store.current = nil
		store.currentBytes = 0
	}
	name := fmt.Sprintf("%s%s-%d%s", filePrefix, time.Now().Format(fileTimeLayout), store.sequence.Add(1), fileSuffix)
	path := filepath.Join(store.dir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("创建请求日志文件: %w", err)
	}
	store.current = file
	store.currentPath = path
	store.currentBytes = 0
	store.prune()
	return nil
}

// handleClear 先落盘已排队的记录，再删除全部日志文件
func (store *Store) handleClear() error {
	for drained := false; !drained; {
		select {
		case entry := <-store.entries:
			store.writeEntry(entry)
		default:
			drained = true
		}
	}
	var errs []error
	if store.current != nil {
		if err := store.current.Close(); err != nil {
			errs = append(errs, err)
		}
		store.current = nil
		store.currentBytes = 0
	}
	store.currentPath = ""
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, entry := range entries {
		if !store.isLogFile(entry) {
			continue
		}
		path := filepath.Join(store.dir, entry.Name())
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("删除 %s: %w", entry.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// prune 删除过期文件，并在总量超限时从最旧文件开始删除
func (store *Store) prune() {
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		slog.Warn("读取请求日志目录失败", "error", err)
		return
	}
	type logFile struct {
		path string
		mod  time.Time
		size int64
	}
	var files []logFile
	var total int64
	for _, entry := range entries {
		if !store.isLogFile(entry) {
			continue
		}
		path := filepath.Join(store.dir, entry.Name())
		if path == store.currentPath {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, logFile{path: path, mod: info.ModTime(), size: info.Size()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })

	deadline := time.Now().Add(-store.retention)
	removed := make(map[string]bool, len(files))
	for _, file := range files {
		if !file.mod.Before(deadline) {
			continue
		}
		if err := os.Remove(file.path); err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("删除过期请求日志失败", "file", file.path, "error", err)
			}
			continue
		}
		removed[file.path] = true
		total -= file.size
	}
	for _, file := range files {
		if total <= store.maxTotalBytes {
			break
		}
		if removed[file.path] {
			continue
		}
		if err := os.Remove(file.path); err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("删除超出上限的请求日志失败", "file", file.path, "error", err)
			}
			continue
		}
		total -= file.size
	}
}

// isLogFile 判断目录项是否为请求日志文件
func (store *Store) isLogFile(entry os.DirEntry) bool {
	if entry.IsDir() {
		return false
	}
	name := entry.Name()
	return strings.HasPrefix(name, filePrefix) && strings.HasSuffix(name, fileSuffix)
}

// warnWriteError 限频记录写入失败
func (store *Store) warnWriteError(err error) {
	if count := store.writeErr.Add(1); count%writeWarnEvery == 1 {
		slog.Warn("写入请求日志失败", "error", err, "failures", count)
	}
}
