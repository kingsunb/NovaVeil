package op

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
)

var (
	// userMu 保护共享的 userCache: 登录热路径读与改密/改名写并发出现,
	// 无锁读写同一 struct 构成 Go 内存模型意义下的数据竞争。
	userMu                  sync.RWMutex
	userCache               model.User
	initialPasswordFilePath string
)

const (
	// initPasswordLen 首次初始化生成的管理员密码长度
	initPasswordLen = 16
	// minPasswordLen 管理员设置新密码的最小长度
	minPasswordLen = 8
	// initialPasswordFilename 是首次启动时保存一次性管理员密码的 owner-only 文件名。
	initialPasswordFilename = "initial-admin-password"
	// passwordAlphabet 生成随机密码使用的字符集(字母+数字)
	passwordAlphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

// UserInit 初始化唯一管理员。首次生成的密码只写入数据目录内权限 0600 的一次性文件，
// 常规日志不包含密码明文；管理员首次改密成功后该文件会被删除。
func UserInit(dataDir ...string) error {
	dir := "data"
	if len(dataDir) > 0 && dataDir[0] != "" {
		dir = dataDir[0]
	}
	initialPasswordFilePath = filepath.Join(dir, initialPasswordFilename)

	userMu.Lock()
	defer userMu.Unlock()

	var stored model.User
	if err := db.GetDB().First(&stored).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("读取管理员失败: %w", err)
		}
		return createInitialUserLocked(dir)
	}
	userCache = stored
	return ensureInitialPasswordFileLocked(dir)
}

// createInitialUserLocked 先建用户再落盘，失败回滚。
func createInitialUserLocked(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建初始密码目录失败: %w", err)
	}
	password, err := GenerateRandomPassword(initPasswordLen)
	if err != nil {
		return err
	}
	user := model.User{
		Username:           "admin",
		Password:           password,
		MustChangePassword: true,
	}
	if err := user.HashPassword(); err != nil {
		return err
	}
	if err := db.GetDB().Create(&user).Error; err != nil {
		return fmt.Errorf("创建管理员失败: %w", err)
	}
	if err := writeInitialPasswordFile(dir, password); err != nil {
		if delErr := db.GetDB().Delete(&model.User{}, user.ID).Error; delErr != nil {
			log.Errorf("initial password file write failed; rollback new user failed: %v", delErr)
		} else {
			userCache = model.User{}
		}
		return err
	}
	userCache = user
	log.Warnf("initial user created; read the one-time password from %s and change it immediately", initialPasswordFilePath)
	return nil
}

// ensureInitialPasswordFileLocked 对已有用户校验一次性密码文件；缺失时重生成密码恢复访问。
func ensureInitialPasswordFileLocked(dir string) error {
	path := filepath.Join(dir, initialPasswordFilename)
	if !userCache.MustChangePassword {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warnf("failed to remove initial password file %s: %v", path, err)
		}
		initialPasswordFilePath = ""
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查初始密码文件失败: %w", err)
	}
	return regenerateInitialPasswordLocked(dir)
}

// regenerateInitialPasswordLocked 为仍处于首次改密状态的用户重生成一次性密码。
func regenerateInitialPasswordLocked(dir string) error {
	oldHash := userCache.Password
	password, err := GenerateRandomPassword(initPasswordLen)
	if err != nil {
		return err
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("计算初始密码哈希失败: %w", err)
	}
	newHash := string(hashed)
	if err := db.GetDB().Model(&model.User{}).Where("id = ?", userCache.ID).Update("password", newHash).Error; err != nil {
		return fmt.Errorf("重置初始管理员密码失败: %w", err)
	}
	if err := writeInitialPasswordFile(dir, password); err != nil {
		if rbErr := db.GetDB().Model(&model.User{}).Where("id = ?", userCache.ID).Update("password", oldHash).Error; rbErr != nil {
			log.Errorf("initial password recovery failed; revert password failed: %v", rbErr)
		}
		return err
	}
	userCache.Password = newHash
	initialPasswordFilePath = filepath.Join(dir, initialPasswordFilename)
	log.Warnf("initial password file missing; generated a new one at %s", initialPasswordFilePath)
	return nil
}

// writeInitialPasswordFile 先写同目录临时文件再重命名，避免半截密码文件。
func writeInitialPasswordFile(dir, password string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建初始密码目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+initialPasswordFilename+"-*")
	if err != nil {
		return fmt.Errorf("创建初始密码临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置初始密码文件权限失败: %w", err)
	}
	if _, err := tmp.WriteString("username: admin\npassword: " + password + "\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入初始密码文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭初始密码文件失败: %w", err)
	}
	target := filepath.Join(dir, initialPasswordFilename)
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("清理旧初始密码文件失败: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("写入初始密码文件失败: %w", err)
	}
	renamed = true
	return nil
}

// GenerateRandomPassword 使用 crypto/rand 生成 n 位随机字母数字密码
func GenerateRandomPassword(n int) (string, error) {
	b := make([]byte, n)
	maxI := big.NewInt(int64(len(passwordAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, maxI)
		if err != nil {
			return "", fmt.Errorf("生成随机密码失败: %w", err)
		}
		b[i] = passwordAlphabet[n.Int64()]
	}
	return string(b), nil
}

// ErrIncorrectOldPassword 表示改密时提供的旧密码不匹配, 调用方应返回 4xx 而非服务器错误。
var ErrIncorrectOldPassword = errors.New("incorrect old password")

// ErrUsernameUnchanged 表示新用户名与原用户名相同, 调用方应返回 4xx 而非服务器错误。
var ErrUsernameUnchanged = errors.New("username unchanged")

// ErrPasswordValidation 表示新密码未通过强度校验, 调用方应返回 400 而非 500。
var ErrPasswordValidation = errors.New("password validation failed")

// ErrInitialPasswordFileCleanupFailed 表示改密已事务提交成功(新密码与新 JWT 密钥均已落库),
// 但一次性初始密码文件删除失败。调用方应视为改密成功: 密码已生效, 仅遗留一个需运维清理
// 的文件, 不能让客户端误以为改密未发生。
var ErrInitialPasswordFileCleanupFailed = errors.New("password changed but initial password file cleanup failed")

// ValidateNewPassword 校验新密码强度，供在线改密与离线重置共用。
func ValidateNewPassword(newPassword string) error {
	// 新密码非空校验: bcrypt 对空串同样可生成/比对通过, 放行等于允许空密码登录。
	if strings.TrimSpace(newPassword) == "" {
		return fmt.Errorf("%w: 新密码不能为空", ErrPasswordValidation)
	}
	if utf8.RuneCountInString(newPassword) < minPasswordLen {
		return fmt.Errorf("%w: 新密码长度至少为 %d 个字符", ErrPasswordValidation, minPasswordLen)
	}
	return nil
}

func UserChangePassword(oldPassword, newPassword string) error {
	if err := ValidateNewPassword(newPassword); err != nil {
		return err
	}

	// 快速预校验: RLock 下验证旧密码, 不阻塞并发读热路径。该检查非权威 —— 两个并发
	// 改密请求可能都通过此处, 权威的 CAS 在写锁下再次验证, 见下。
	userMu.RLock()
	if err := userCache.ComparePassword(oldPassword); err != nil {
		userMu.RUnlock()
		return ErrIncorrectOldPassword
	}
	userMu.RUnlock()

	// 写操作持 WriteLock: 在锁内完成 CAS 重新验证与事务提交, 提交成功后再发布缓存。
	userMu.Lock()
	defer userMu.Unlock()

	// CAS: 写锁下再次验证旧密码 hash。两个并发改密请求即便都通过了上面的 RLock 预校验,
	// 也会在此处串行: 先提交者把 userCache.Password 改为新 hash, 后提交者用同一旧密码
	// 比对新 hash 必然失败, 从而只允许一个成功, 杜绝双重改密。
	if err := userCache.ComparePassword(oldPassword); err != nil {
		return ErrIncorrectOldPassword
	}

	// 事务内原子完成: 新密码 hash 计算、用户表更新、JWT 密钥轮换。三者同生共死,
	// 任一失败整体回滚, 不再出现"JWT 已轮换 + 缓存已改, 而 DB 用户表仍是旧值"的分裂。
	var newPasswordHash, newSecret string
	if err := db.GetDB().Transaction(func(tx *gorm.DB) error {
		hashed, e := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if e != nil {
			return fmt.Errorf("计算新密码哈希失败: %w", e)
		}
		newPasswordHash = string(hashed)

		secret, e := randomHexSecret(jwtSecretBytes)
		if e != nil {
			return fmt.Errorf("生成 JWT 密钥失败: %w", e)
		}
		newSecret = secret

		// 先写用户表, 再写 JWT 密钥; 任一失败事务回滚, 用户表与密钥表保持一致。
		// 不传 &userCache 给 GORM：v1.31 的 Updates 会通过指针回写字段到 model，
		// 事务回滚只回滚 DB 不回滚内存，导致 userCache 被污染（半更新）。
		if e := tx.Model(&model.User{}).Where("id = ?", userCache.ID).Updates(map[string]interface{}{
			"password":             newPasswordHash,
			"must_change_password": false,
		}).Error; e != nil {
			return fmt.Errorf("更新密码失败: %w", e)
		}
		if e := authJWTSecretSaveTx(tx, newSecret); e != nil {
			return fmt.Errorf("轮换 JWT 密钥失败: %w", e)
		}
		return nil
	}); err != nil {
		// 事务回滚: userCache 与 settingCache 均未变更, DB 用户表与密钥表保持旧值, 状态一致。
		return err
	}

	// DB 已提交: 按一致锁顺序发布缓存(先 DB 提交成功, 再更新缓存), 保证缓存永不领先于 DB,
	// 重启后从 DB 重载的状态与提交前 API 返回的结果一致。
	userCache.Password = newPasswordHash
	userCache.MustChangePassword = false
	settingCache.Set(model.SettingKeyAuthJWTSecret, newSecret)

	// 初始密码文件清理: 改密已提交成功, 文件删除失败作为"提交成功但清理失败"的独立结果,
	// 不让客户端误以为改密没发生。先清空路径再尝试删除, 避免遗留路径在后续改密中重复失败。
	if initialPasswordFilePath != "" {
		path := initialPasswordFilePath
		initialPasswordFilePath = ""
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			log.Warnf("password changed but failed to remove initial password file %s: %v", path, rmErr)
			return ErrInitialPasswordFileCleanupFailed
		}
	}
	return nil
}

// UserResetPassword 离线重置唯一管理员密码，并轮换 JWT 密钥、清除首次改密标记。
func UserResetPassword(newPassword string, dataDir ...string) error {
	if err := ValidateNewPassword(newPassword); err != nil {
		return err
	}
	dir := "data"
	if len(dataDir) > 0 && dataDir[0] != "" {
		dir = dataDir[0]
	}

	userMu.Lock()
	defer userMu.Unlock()

	var user model.User
	if err := db.GetDB().First(&user).Error; err != nil {
		return fmt.Errorf("加载管理员失败: %w", err)
	}

	var newPasswordHash, newSecret string
	if err := db.GetDB().Transaction(func(tx *gorm.DB) error {
		hashed, e := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if e != nil {
			return fmt.Errorf("计算新密码哈希失败: %w", e)
		}
		newPasswordHash = string(hashed)

		secret, e := randomHexSecret(jwtSecretBytes)
		if e != nil {
			return fmt.Errorf("生成 JWT 密钥失败: %w", e)
		}
		newSecret = secret

		if e := tx.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
			"password":             newPasswordHash,
			"must_change_password": false,
		}).Error; e != nil {
			return fmt.Errorf("更新密码失败: %w", e)
		}
		if e := authJWTSecretSaveTx(tx, newSecret); e != nil {
			return fmt.Errorf("轮换 JWT 密钥失败: %w", e)
		}
		return nil
	}); err != nil {
		return err
	}

	user.Password = newPasswordHash
	user.MustChangePassword = false
	userCache = user
	settingCache.Set(model.SettingKeyAuthJWTSecret, newSecret)

	initialPasswordFilePath = ""
	path := filepath.Join(dir, initialPasswordFilename)
	if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		log.Warnf("offline password reset but failed to remove initial password file %s: %v", path, rmErr)
	}
	return nil
}

func UserChangeUsername(newUsername, password string) error {
	userMu.Lock()
	defer userMu.Unlock()
	// 安全校验: 修改用户名前必须验证当前密码, 防止会话被劫持后篡改登录凭据。
	if err := userCache.ComparePassword(password); err != nil {
		return ErrIncorrectOldPassword
	}
	if userCache.Username == newUsername {
		return ErrUsernameUnchanged
	}
	// 事务内先写 DB, 提交成功后再更新缓存, 保证缓存不领先于 DB。
	if err := db.GetDB().Transaction(func(tx *gorm.DB) error {
		return tx.Model(&userCache).Update("username", newUsername).Error
	}); err != nil {
		return fmt.Errorf("更新用户名失败: %w", err)
	}
	userCache.Username = newUsername
	return nil
}

// ErrInitialPasswordFileConsumeFailed 表示首次登录成功后一次性初始密码文件删除失败。
// 与改密清理一样, 登录本身已成功, 仅遗留一个需运维处理的文件。
var ErrInitialPasswordFileConsumeFailed = errors.New("login succeeded but initial password file cleanup failed")

// UserConsumeInitialPasswordFile 在初始管理员首次成功登录后删除一次性密码文件,
// 防止种子密码文件长期留盘(审计 SEC-08)。幂等、可安全重复调用。
func UserConsumeInitialPasswordFile() error {
	userMu.Lock()
	defer userMu.Unlock()
	if initialPasswordFilePath == "" {
		return nil
	}
	path := initialPasswordFilePath
	initialPasswordFilePath = ""
	if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		log.Warnf("login succeeded but failed to remove initial password file %s: %v", path, rmErr)
		return ErrInitialPasswordFileConsumeFailed
	}
	return nil
}

func UserVerify(username, password string) error {
	userMu.RLock()
	defer userMu.RUnlock()
	// 用户名与密码错误返回同一文案, 不向登录方区分两者, 压缩用户名枚举面;
	// 用户名不匹配时也执行一次同代价的 bcrypt 比对, 抹平"用户名是否存在"
	// 的响应时间差, 文案与时序侧信道一并封死。
	if username != userCache.Username {
		_ = userCache.ComparePassword(password)
		return fmt.Errorf("用户名或密码错误")
	}
	if err := userCache.ComparePassword(password); err != nil {
		return fmt.Errorf("用户名或密码错误")
	}
	return nil
}

func UserGet() model.User {
	userMu.RLock()
	defer userMu.RUnlock()
	return userCache
}
