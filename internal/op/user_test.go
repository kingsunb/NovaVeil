package op

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/testutil"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	cleanup, err := testutil.InitTestDB()
	if err != nil {
		panic(err)
	}
	defer cleanup()
	if err := settingRefreshCache(context.Background()); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func hashForTest(password string) (string, error) {
	u := model.User{Password: password}
	if err := u.HashPassword(); err != nil {
		return "", err
	}
	return u.Password, nil
}

// resetTestUser 用已知密码准备唯一的管理员行, 并同步 op 层用户缓存

func TestUserConsumeInitialPasswordFileRemovesSeed(t *testing.T) {
	// SEC-08/L6: 首次登录成功后一次性初始密码文件必须从磁盘删除。
	// 若删除失败(权限等)返回 ErrInitialPasswordFileConsumeFailed, 由登录 handler 记录。
	oldPath := initialPasswordFilePath
	t.Cleanup(func() { initialPasswordFilePath = oldPath })

	dir := t.TempDir()
	path := filepath.Join(dir, initialPasswordFilename)
	if err := os.WriteFile(path, []byte("username: admin\npassword: seed-pass-123\n"), 0o600); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	initialPasswordFilePath = path

	if err := UserConsumeInitialPasswordFile(); err != nil {
		t.Fatalf("UserConsumeInitialPasswordFile: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("seed file must be removed after consume, stat err=%v", err)
	}
	if initialPasswordFilePath != "" {
		t.Fatalf("initialPasswordFilePath = %q, want empty after consume", initialPasswordFilePath)
	}

	// 幂等: 再次消费没有文件可删也不应报错。
	if err := UserConsumeInitialPasswordFile(); err != nil {
		t.Fatalf("second UserConsumeInitialPasswordFile: %v", err)
	}
}
func resetTestUser(t *testing.T, password string, mustChange bool) {
	t.Helper()
	hashed, err := hashForTest(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	var stored model.User
	if err := db.GetDB().First(&stored).Error; err != nil {
		stored = model.User{Username: "admin", Password: hashed, MustChangePassword: mustChange}
		if err := db.GetDB().Create(&stored).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	} else {
		stored.Password = hashed
		stored.MustChangePassword = mustChange
		if err := db.GetDB().Save(&stored).Error; err != nil {
			t.Fatalf("save user: %v", err)
		}
	}
	userCache = stored
}

func TestGenerateRandomPasswordFormat(t *testing.T) {
	const length = 16
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		pwd, err := GenerateRandomPassword(length)
		if err != nil {
			t.Fatalf("GenerateRandomPassword error: %v", err)
		}
		if len(pwd) != length {
			t.Fatalf("password length = %d, want %d", len(pwd), length)
		}
		for _, ch := range pwd {
			if !isPasswordAlphabetChar(byte(ch)) {
				t.Fatalf("password %q contains non-alphanumeric char %q", pwd, ch)
			}
		}
		seen[pwd] = struct{}{}
	}
	// 熵检查: 100 次 16 位字母数字随机生成不应出现重复
	if len(seen) != 100 {
		t.Fatalf("expected 100 unique passwords, got %d", len(seen))
	}
}

func isPasswordAlphabetChar(c byte) bool {
	for i := 0; i < len(passwordAlphabet); i++ {
		if passwordAlphabet[i] == c {
			return true
		}
	}
	return false
}

func TestUserInitCreatesMustChangeFlag(t *testing.T) {
	resetTestUser(t, "seed-password", false)
	// 清空用户表模拟首次启动
	if err := db.GetDB().Exec("DELETE FROM users").Error; err != nil {
		t.Fatalf("clear users: %v", err)
	}
	userCache = model.User{}

	bootstrapDir := t.TempDir()
	initialPasswordFilePath = ""
	if err := UserInit(bootstrapDir); err != nil {
		t.Fatalf("UserInit error: %v", err)
	}
	bootstrapPath := filepath.Join(bootstrapDir, initialPasswordFilename)
	info, err := os.Stat(bootstrapPath)
	if err != nil {
		t.Fatalf("bootstrap file missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bootstrap mode = %o, want 600", info.Mode().Perm())
	}
	contents, err := os.ReadFile(bootstrapPath)
	if err != nil || !strings.Contains(string(contents), "username: admin") || !strings.Contains(string(contents), "password: ") {
		t.Fatalf("invalid bootstrap file: %q err=%v", contents, err)
	}
	user := UserGet()
	if user.Username != "admin" {
		t.Fatalf("username = %q, want admin", user.Username)
	}
	if !user.MustChangePassword {
		t.Fatal("fresh init must set MustChangePassword = true")
	}
	// 固定默认密码 admin 不应再能通过校验
	if err := user.ComparePassword("admin"); err == nil {
		t.Fatal("initial password must not be the legacy constant 'admin'")
	}

	var stored model.User
	if err := db.GetDB().First(&stored).Error; err != nil {
		t.Fatalf("load stored user: %v", err)
	}
	if !stored.MustChangePassword {
		t.Fatal("must_change_password column not persisted")
	}

	// 二次调用应为幂等
	if err := UserInit(bootstrapDir); err != nil {
		t.Fatalf("second UserInit error: %v", err)
	}
	if got := UserGet(); got.ID != user.ID {
		t.Fatal("UserInit must be idempotent")
	}
}

func TestUserChangePasswordClearsFlagAndRotatesSecret(t *testing.T) {
	resetTestUser(t, "old-password-1", true)
	bootstrapPath := filepath.Join(t.TempDir(), initialPasswordFilename)
	if err := os.WriteFile(bootstrapPath, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	initialPasswordFilePath = bootstrapPath
	before, err := AuthJWTSecretGet() // 确保密钥已存在, 便于对比轮换前后
	if err != nil {
		t.Fatalf("ensure secret: %v", err)
	}

	if err := UserChangePassword("old-password-1", "new-password-2"); err != nil {
		t.Fatalf("UserChangePassword error: %v", err)
	}

	if userCache.MustChangePassword {
		t.Fatal("MustChangePassword must be cleared after change")
	}
	var stored model.User
	if err := db.GetDB().First(&stored).Error; err != nil {
		t.Fatalf("load stored user: %v", err)
	}
	if stored.MustChangePassword {
		t.Fatal("must_change_password flag must be cleared in DB")
	}
	if err := userCache.ComparePassword("new-password-2"); err != nil {
		t.Fatalf("new password does not verify: %v", err)
	}

	after, err := settingGetInternal(model.SettingKeyAuthJWTSecret)
	if err != nil {
		t.Fatalf("read rotated secret: %v", err)
	}
	if after == before {
		t.Fatal("jwt secret must be rotated on password change")
	}
	if _, err := os.Stat(bootstrapPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap file must be removed after password change, err=%v", err)
	}
}

func TestUserChangePasswordRemovesBootstrapFileAfterRestart(t *testing.T) {
	resetTestUser(t, "old-password-bootstrap", true)
	dir := t.TempDir()
	path := filepath.Join(dir, initialPasswordFilename)
	if err := os.WriteFile(path, []byte("bootstrap"), 0o600); err != nil {
		t.Fatalf("write bootstrap file: %v", err)
	}

	// 模拟进程重启：UserInit 从数据库读取已有用户，并恢复 bootstrap 文件路径。
	initialPasswordFilePath = ""
	if err := UserInit(dir); err != nil {
		t.Fatalf("UserInit: %v", err)
	}
	if initialPasswordFilePath != path {
		t.Fatalf("initial password path = %q, want %q", initialPasswordFilePath, path)
	}
	if err := UserChangePassword("old-password-bootstrap", "new-password-bootstrap"); err != nil {
		t.Fatalf("UserChangePassword: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap file should be removed, stat err = %v", err)
	}
}

func TestSettingListHidesJWTSecret(t *testing.T) {
	if _, err := AuthJWTSecretGet(); err != nil {
		t.Fatalf("ensure secret: %v", err)
	}
	settings, err := SettingList(context.Background())
	if err != nil {
		t.Fatalf("SettingList: %v", err)
	}
	for _, s := range settings {
		if s.Key == model.SettingKeyAuthJWTSecret {
			t.Fatal("settings list must not expose jwt secret")
		}
	}
}

func TestSettingValidateRejectsJWTSecretWrite(t *testing.T) {
	s := model.Setting{Key: model.SettingKeyAuthJWTSecret, Value: "attacker-controlled"}
	if err := s.Validate(); err == nil {
		t.Fatal("jwt secret must not be writable via settings API")
	}
}

// errInjectedDBFailure 供 STA-02 原子性测试: 注入到 GORM Create 回调中迫使事务内
// JWT 密钥保存失败, 验证用户表更新随之回滚, 不发生半更新。
var errInjectedDBFailure = errors.New("injected DB write failure for test")

// TestUserChangePasswordConcurrentOnlyOneSucceeds 验证 CAS: 两个并发改密请求携带
// 同一旧密码时只允许一个成功, 另一个以 ErrIncorrectOldPassword 失败, 杜绝双重改密。
func TestUserChangePasswordConcurrentOnlyOneSucceeds(t *testing.T) {
	resetTestUser(t, "concurrent-old", true)
	initialPasswordFilePath = ""
	if _, err := AuthJWTSecretGet(); err != nil {
		t.Fatalf("ensure secret: %v", err)
	}

	// 两个并发改密请求携带同一旧密码: RLock 预校验可能都通过, 但写锁下的 CAS
	// 串行重新验证旧 hash, 先提交者改写 userCache.Password 为新 hash, 后提交者
	// 用同一旧密码比对新 hash 必然失败, 只允许一个成功。
	var wg sync.WaitGroup
	var errs [2]error
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = UserChangePassword("concurrent-old", fmt.Sprintf("concurrent-new-%d", idx))
		}(i)
	}
	wg.Wait()

	successCount := 0
	for _, e := range errs {
		if e == nil {
			successCount++
		}
	}
	if successCount != 1 {
		t.Fatalf("expected exactly one concurrent change to succeed, got %d (errs=%v)", successCount, errs)
	}
	for _, e := range errs {
		if e != nil && !errors.Is(e, ErrIncorrectOldPassword) {
			t.Fatalf("concurrent loser must fail with ErrIncorrectOldPassword, got: %v", e)
		}
	}
}

// TestUserChangePasswordDBFailureNoHalfUpdate 注入 DB 写入失败(事务内 JWT 密钥保存
// 失败), 验证用户表更新随之回滚、缓存与密钥均未变更, 不出现"半更新"分裂状态。
func TestUserChangePasswordDBFailureNoHalfUpdate(t *testing.T) {
	resetTestUser(t, "dbfail-old", true)
	initialPasswordFilePath = ""
	if _, err := AuthJWTSecretGet(); err != nil {
		t.Fatalf("ensure secret: %v", err)
	}
	secretBefore, err := settingGetInternal(model.SettingKeyAuthJWTSecret)
	if err != nil {
		t.Fatalf("read secret before: %v", err)
	}
	var userBefore model.User
	if err := db.GetDB().First(&userBefore).Error; err != nil {
		t.Fatalf("load user before: %v", err)
	}
	cacheBefore := userCache

	// 注入 DB 写入失败: 在事务内 JWT 密钥保存(Create on settings)时返回错误,
	// 迫使第二条写操作失败、第一条(用户表更新)必须随事务回滚, 验证不发生半更新。
	createCB := db.GetDB().Callback().Create()
	const cbName = "test_fail_jwt_save"
	if err := createCB.Before("gorm:create").Register(cbName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "settings" {
			tx.Error = errInjectedDBFailure
		}
	}); err != nil {
		t.Fatalf("register failing callback: %v", err)
	}
	defer func() {
		if err := createCB.Remove(cbName); err != nil {
			t.Errorf("remove failing callback: %v", err)
		}
	}()

	err = UserChangePassword("dbfail-old", "dbfail-new")
	if err == nil {
		t.Fatal("expected error when JWT secret save fails, got nil")
	}

	// 缓存不变: 事务回滚后未发布缓存, 不出现"缓存已改而 DB 未改"的半更新。
	if userCache.Password != cacheBefore.Password {
		t.Fatalf("cache password changed despite DB failure: got %q want %q", userCache.Password, cacheBefore.Password)
	}
	if userCache.MustChangePassword != cacheBefore.MustChangePassword {
		t.Fatal("cache must_change_password changed despite DB failure")
	}
	secretAfter, err := settingGetInternal(model.SettingKeyAuthJWTSecret)
	if err != nil {
		t.Fatalf("read secret after: %v", err)
	}
	if secretAfter != secretBefore {
		t.Fatalf("jwt secret cache changed despite DB failure: got %q want %q", secretAfter, secretBefore)
	}

	// DB 用户表回滚: 第一条写操作(密码更新)随事务回滚, 仍是旧密码。
	var userAfter model.User
	if err := db.GetDB().First(&userAfter).Error; err != nil {
		t.Fatalf("load user after: %v", err)
	}
	if userAfter.Password != userBefore.Password {
		t.Fatal("user password must be rolled back when JWT save fails (no half-update)")
	}
	if userAfter.MustChangePassword != userBefore.MustChangePassword {
		t.Fatal("user must_change_password must be rolled back when JWT save fails")
	}
}
