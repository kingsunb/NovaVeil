package op

import (
	"encoding/hex"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestAuthJWTSecretGeneratedAndPersisted(t *testing.T) {
	resetTestUser(t, "secret-user-1", false)

	secret, err := AuthJWTSecretGet()
	if err != nil {
		t.Fatalf("AuthJWTSecretGet error: %v", err)
	}
	if len(secret) != 64 {
		t.Fatalf("secret length = %d, want 64 hex chars (32 bytes)", len(secret))
	}
	raw, err := hex.DecodeString(secret)
	if err != nil {
		t.Fatalf("secret is not valid hex: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("decoded secret = %d bytes, want 32", len(raw))
	}

	// 复用: 二次读取应返回同一密钥
	again, err := AuthJWTSecretGet()
	if err != nil {
		t.Fatalf("second AuthJWTSecretGet error: %v", err)
	}
	if again != secret {
		t.Fatal("jwt secret must be reused once generated")
	}

	// 持久化: 缓存与 DB 一致
	cached, err := settingGetInternal(model.SettingKeyAuthJWTSecret)
	if err != nil || cached != secret {
		t.Fatalf("cache/DB mismatch: cached=%q err=%v", cached, err)
	}
}

func TestAuthJWTRotateSecret(t *testing.T) {
	resetTestUser(t, "secret-user-2", false)

	old, err := AuthJWTSecretGet()
	if err != nil {
		t.Fatalf("ensure secret: %v", err)
	}
	if err := AuthJWTRotateSecret(); err != nil {
		t.Fatalf("AuthJWTRotateSecret error: %v", err)
	}
	fresh, err := AuthJWTSecretGet()
	if err != nil {
		t.Fatalf("read after rotate: %v", err)
	}
	if fresh == old {
		t.Fatal("rotated secret must differ from previous")
	}
	if len(fresh) != 64 {
		t.Fatalf("rotated secret length = %d, want 64", len(fresh))
	}
	cached, err := settingGetInternal(model.SettingKeyAuthJWTSecret)
	if err != nil || cached != fresh {
		t.Fatalf("cache not updated after rotation: cached=%q err=%v", cached, err)
	}
}

func TestFilterSecretSettings(t *testing.T) {
	rows := []model.Setting{
		{Key: model.SettingKeyProxyPool, Value: `[{"url":"http://127.0.0.1:9000"}]`},
		{Key: model.SettingKeyProxyURL, Value: "http://127.0.0.1:7890"},
		{Key: model.SettingKeyAuthJWTSecret, Value: "top-secret"},
		{Key: model.SettingKeyCORSAllowOrigins, Value: "*"},
	}
	filtered := filterSecretSettings(rows)
	if len(filtered) != 1 {
		t.Fatalf("filtered len = %d, want 1", len(filtered))
	}
	for _, s := range filtered {
		switch s.Key {
		case model.SettingKeyAuthJWTSecret, model.SettingKeyProxyURL, model.SettingKeyProxyPool:
			t.Fatalf("secret setting %q must be filtered out", s.Key)
		}
	}
}
