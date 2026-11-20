package op

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
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

func TestAuthJWTStateRevokesAfterCommittedChange(t *testing.T) {
	for _, name := range []string{"logout", "password-change", "password-reset"} {
		t.Run(name, func(t *testing.T) {
			resetTestUser(t, "state-old-password", false)
			initialPasswordFilePath = ""
			before, err := AuthJWTStateGet()
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "logout":
				err = AuthJWTRotateSecret()
			case "password-change":
				err = UserChangePassword("state-old-password", "state-new-password")
			case "password-reset":
				err = UserResetPassword("state-new-password", t.TempDir())
			}
			if err != nil {
				t.Fatal(err)
			}
			if !errors.Is(before.Revoked.Err(), context.Canceled) {
				t.Fatal("committed secret change must revoke the previous state")
			}
			after, err := AuthJWTStateGet()
			if err != nil {
				t.Fatal(err)
			}
			if after.Secret == before.Secret || after.Revoked == before.Revoked || after.Revoked.Err() != nil {
				t.Fatal("committed change must publish a fresh active state")
			}
			persisted, err := loadSettingFromDB(model.SettingKeyAuthJWTSecret)
			if err != nil || persisted != after.Secret {
				t.Fatalf("committed secret and cache disagree: %v", err)
			}
		})
	}
}

func TestAuthJWTStateSurvivesFailedChange(t *testing.T) {
	for _, name := range []string{"logout", "password-change", "password-reset"} {
		t.Run(name, func(t *testing.T) {
			resetTestUser(t, "failed-old-password", false)
			initialPasswordFilePath = ""
			before, err := AuthJWTStateGet()
			if err != nil {
				t.Fatal(err)
			}
			createCB := db.GetDB().Callback().Create()
			const callbackName = "test_auth_state_failed_change"
			if err := createCB.Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "settings" {
					tx.Error = errInjectedDBFailure
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := createCB.Remove(callbackName); err != nil {
					t.Errorf("remove failing callback: %v", err)
				}
			})
			switch name {
			case "logout":
				err = AuthJWTRotateSecret()
			case "password-change":
				err = UserChangePassword("failed-old-password", "failed-new-password")
			case "password-reset":
				err = UserResetPassword("failed-new-password", t.TempDir())
			}
			if !errors.Is(err, errInjectedDBFailure) {
				t.Fatalf("expected storage failure, got %v", err)
			}
			after, err := AuthJWTStateGet()
			if err != nil {
				t.Fatal(err)
			}
			if after.Secret != before.Secret || after.Revoked != before.Revoked || before.Revoked.Err() != nil {
				t.Fatal("failed change must preserve the active state")
			}
			if err := UserVerify("admin", "failed-old-password"); err != nil {
				t.Fatalf("failed change must preserve credentials: %v", err)
			}
			persisted, err := loadSettingFromDB(model.SettingKeyAuthJWTSecret)
			if err != nil || persisted != before.Secret {
				t.Fatalf("failed change altered the persisted secret: %v", err)
			}
		})
	}
}

func TestAuthJWTStateRefreshOnlyRevokesChangedSecret(t *testing.T) {
	before, err := AuthJWTStateGet()
	if err != nil {
		t.Fatal(err)
	}
	if err := settingRefreshCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	unchanged, err := AuthJWTStateGet()
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Secret != before.Secret || unchanged.Revoked != before.Revoked || before.Revoked.Err() != nil {
		t.Fatal("refreshing an unchanged secret must preserve active sessions")
	}
	updatedSecret, err := randomHexSecret(jwtSecretBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := authJWTSecretSaveTx(db.GetDB(), updatedSecret); err != nil {
		t.Fatal(err)
	}
	if err := settingRefreshCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := AuthJWTStateGet()
	if err != nil {
		t.Fatal(err)
	}
	if after.Secret != updatedSecret || after.Revoked.Err() != nil || before.Revoked.Err() == nil {
		t.Fatal("refreshing a changed secret must revoke the previous state")
	}
}

func TestAuthSecretConcurrentPasswordChangeAndLogout(t *testing.T) {
	resetTestUser(t, "concurrent-auth-old", false)
	initialPasswordFilePath = ""
	before, err := AuthJWTStateGet()
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		results <- UserChangePassword("concurrent-auth-old", "concurrent-auth-new")
	}()
	go func() {
		<-start
		results <- AuthJWTRotateSecret()
	}()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Errorf("concurrent auth change: %v", err)
		}
	}
	after, err := AuthJWTStateGet()
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := loadSettingFromDB(model.SettingKeyAuthJWTSecret)
	if err != nil || persisted != after.Secret {
		t.Fatalf("concurrent changes left inconsistent secret state: %v", err)
	}
	if before.Revoked.Err() == nil || after.Revoked.Err() != nil {
		t.Fatal("only the committed final state may remain active")
	}
	if err := UserVerify("admin", "concurrent-auth-new"); err != nil {
		t.Fatalf("password change was lost: %v", err)
	}
}
