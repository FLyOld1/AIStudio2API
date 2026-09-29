package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRemoteAccessRequiresKey(t *testing.T) {
	cfg := Default()
	cfg.AdminRemoteAccess = true
	if err := cfg.Validate(); err == nil {
		t.Fatalf("ADMIN_REMOTE_ACCESS 缺少 ADMIN_API_KEY 时应校验失败")
	}
	cfg.AdminAPIKey = "secret"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("配置有效时不应报错: %v", err)
	}
}

func TestValidateRequestLogParams(t *testing.T) {
	cfg := Default()
	cfg.RequestLogMaxFileMB = 100
	cfg.RequestLogMaxTotalMB = 50
	if err := cfg.Validate(); err == nil {
		t.Fatalf("总量上限小于单文件上限时应校验失败")
	}
	cfg.RequestLogMaxTotalMB = 200
	cfg.RequestLogBodyLimitKB = 0
	if err := cfg.Validate(); err == nil {
		t.Fatalf("正文上限为零时应校验失败")
	}
	cfg.RequestLogBodyLimitKB = 256
	cfg.RequestLogDir = " "
	if err := cfg.Validate(); err == nil {
		t.Fatalf("日志目录为空时应校验失败")
	}
	cfg.RequestLogDir = "logs"
	cfg.RequestLogEnabled = false
	cfg.RequestLogMaxFileMB = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("关闭日志时不应校验请求日志参数: %v", err)
	}
}

func TestConfigJSONRoundTrip(t *testing.T) {
	cfg := Default()
	cfg.AdminAPIKey = "secret"
	cfg.AdminRemoteAccess = true
	cfg.RequestLogDir = "custom-logs"
	cfg.RequestLogMaxFileMB = 16
	cfg.RequestLogMaxTotalMB = 128
	cfg.RequestLogRetentionDays = 3
	cfg.RequestLogBodyLimitKB = 64

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var parsed Config
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if parsed.AdminAPIKey != "secret" || !parsed.AdminRemoteAccess {
		t.Fatalf("管理密钥字段往返失败: %+v", parsed)
	}
	if parsed.RequestLogDir != "custom-logs" || parsed.RequestLogMaxFileMB != 16 ||
		parsed.RequestLogMaxTotalMB != 128 || parsed.RequestLogRetentionDays != 3 ||
		parsed.RequestLogBodyLimitKB != 64 || !parsed.RequestLogEnabled {
		t.Fatalf("请求日志字段往返失败: %+v", parsed)
	}
}

func TestSaveLoadNewKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	cfg := Default()
	cfg.AdminAPIKey = "secret"
	cfg.AdminRemoteAccess = true
	cfg.RequestLogDir = "custom-logs"
	cfg.RequestLogMaxFileMB = 16
	cfg.RequestLogMaxTotalMB = 128
	cfg.RequestLogRetentionDays = 3
	cfg.RequestLogBodyLimitKB = 64
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.AdminAPIKey != "secret" || !loaded.AdminRemoteAccess {
		t.Fatalf("管理密钥字段读写失败: %+v", loaded)
	}
	if loaded.RequestLogDir != "custom-logs" || loaded.RequestLogMaxFileMB != 16 ||
		loaded.RequestLogMaxTotalMB != 128 || loaded.RequestLogRetentionDays != 3 ||
		loaded.RequestLogBodyLimitKB != 64 || !loaded.RequestLogEnabled {
		t.Fatalf("请求日志字段读写失败: %+v", loaded)
	}
}

func TestLoadRejectsInvalidRemoteAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "AISTUDIO_AUTH_STATES=auth\nADMIN_REMOTE_ACCESS=maybe\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入测试配置: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("非法布尔值应报错")
	}
}
