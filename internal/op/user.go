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
	if err := db.GetDB().First(&userCache).Error; err == nil {
		if !userCache.MustChangePassword {
			_ = os.Remove(initialPasswordFilePath)
			initialPasswordFilePath = ""
		}
		return nil
	}
	password, err := GenerateRandomPassword(initPasswordLen)
	if err != nil {
		return err
	}
	userCache = model.User{
		Username:           "admin",
		Password:           password,
		MustChangePassword: true,
	}
	if err := userCache.HashPassword(); err != nil {
		return err
	}
	if err := db.GetDB().Create(&userCache).Error; err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建初始密码目录失败: %w", err)
	}
	path := initialPasswordFilePath
	if err := os.WriteFile(path, []byte("username: admin\npassword: "+password+"\n"), 0o600); err != nil {
		return fmt.Errorf("写入初始密码文件失败: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("设置初始密码文件权限失败: %w", err)
	}
	initialPasswordFilePath = path
	log.Warnf("initial user created; read the one-time password from %s and change it immediately", path)
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

func UserChangePassword(oldPassword, newPassword string) error {
	// 新密码非空校验: bcrypt 对空串同样可生成/比对通过, 放行等于允许空密码登录。
	if strings.TrimSpace(newPassword) == "" {
		return fmt.Errorf("%w: 新密码不能为空", ErrPasswordValidation)
	}
	// 密码强度校验: 至少 8 个字符, 防止弱密码被设置。
	if utf8.RuneCountInString(newPassword) < 8 {
		return fmt.Errorf("%w: 新密码长度至少为 8 个字符", ErrPasswordValidation)
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
