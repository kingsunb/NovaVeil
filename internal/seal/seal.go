// Package seal 提供字段级静态加密(AES-256-GCM)原语。
//
// 设计目标: API Key / 渠道 Key / 代理凭据等敏感字段在数据库中以密文存储,
// 进程内存中仍以明文缓存与使用。未加前缀的存量明文在读取时按迁移兼容处理,
// 新写入恒为密文。
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/charmbracelet/log"
)

// VersionPrefix 是密文字符串的版本前缀; 无前缀值一律视为存量明文。
const VersionPrefix = "nv1:"

const (
	nonceSize = 12
	keySize   = 32
)

var (
	mu  sync.RWMutex
	gcm cipher.AEAD
)

// Configure 初始化加密密钥。key 非空时优先使用配置密钥(hex 或任意字符串派生);
// 否则从 keyFile 加载 32 字节 hex 密钥, keyFile 不存在时生成并以 0600 权限保存,
// 保证重启后可用同一密钥解密旧数据。
func Configure(key, keyFile string) error {
	raw, err := resolveKey(key, keyFile)
	if err != nil {
		return err
	}
	g, err := newGCM(raw)
	if err != nil {
		return err
	}
	mu.Lock()
	gcm = g
	mu.Unlock()
	return nil
}

// Seal 加密明文, 返回带版本前缀的密文。空串原样返回(空值无需加密, 也便于 GORM 跳过)。
func Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	g, err := currentGCM()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成加密 nonce 失败: %w", err)
	}
	sealed := g.Seal(nonce, nonce, []byte(plaintext), nil)
	return VersionPrefix + base64.URLEncoding.EncodeToString(sealed), nil
}

// Open 解密密文; 无版本前缀的值按存量明文返回(迁移兼容)。
func Open(stored string) (string, error) {
	if stored == "" || !strings.HasPrefix(stored, VersionPrefix) {
		return stored, nil
	}
	raw, err := base64.URLEncoding.DecodeString(strings.TrimPrefix(stored, VersionPrefix))
	if err != nil {
		return "", fmt.Errorf("解密失败, 密文编码非法: %w", err)
	}
	if len(raw) < nonceSize {
		return "", fmt.Errorf("解密失败, 密文长度非法")
	}
	g, err := currentGCM()
	if err != nil {
		return "", err
	}
	plain, err := g.Open(nil, raw[:nonceSize], raw[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("解密失败, 密钥或数据已损坏: %w", err)
	}
	return string(plain), nil
}

// IsSealed 报告值是否为密文(仅检查前缀, 不校验内容)。
func IsSealed(stored string) bool {
	return strings.HasPrefix(stored, VersionPrefix)
}

func currentGCM() (cipher.AEAD, error) {
	mu.RLock()
	g := gcm
	mu.RUnlock()
	if g != nil {
		return g, nil
	}

	// 未显式 Configure 时回退到进程内随机临时密钥, 保证单元测试与未初始化路径
	// 可运行; 生产启动路径必须 Configure, 否则重启后无法解密旧密文。
	mu.Lock()
	defer mu.Unlock()
	if gcm != nil {
		return gcm, nil
	}
	var raw [keySize]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, fmt.Errorf("生成临时加密密钥失败: %w", err)
	}
	g, err := newGCM(raw[:])
	if err != nil {
		return nil, err
	}
	gcm = g
	return gcm, nil
}

func newGCM(raw []byte) (cipher.AEAD, error) {
	if len(raw) != keySize {
		return nil, fmt.Errorf("加密密钥长度必须为 %d 字节", keySize)
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func resolveKey(key, keyFile string) ([]byte, error) {
	if key != "" {
		return deriveKey(key), nil
	}
	if keyFile == "" {
		var raw [keySize]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, fmt.Errorf("生成临时加密密钥失败: %w", err)
		}
		return raw[:], nil
	}

	if data, err := os.ReadFile(keyFile); err == nil {
		trimmed := strings.TrimSpace(string(data))
		raw, decErr := hex.DecodeString(trimmed)
		if decErr != nil || len(raw) != keySize {
			return nil, fmt.Errorf("加密密钥文件 %s 内容非法: 需为 %d 字节 hex", keyFile, keySize*2)
		}
		return raw, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取加密密钥文件 %s 失败: %w", keyFile, err)
	}

	var raw [keySize]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, fmt.Errorf("生成加密密钥失败: %w", err)
	}
	if dir := filepath.Dir(keyFile); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建加密密钥目录失败: %w", err)
		}
	}
	encoded := hex.EncodeToString(raw[:])
	if err := os.WriteFile(keyFile, []byte(encoded+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("写入加密密钥文件失败: %w", err)
	}
	if err := os.Chmod(keyFile, 0o600); err != nil {
		return nil, fmt.Errorf("设置加密密钥文件权限失败: %w", err)
	}
	// 静默换新密钥会让存量 nv1: 密文全部不可解。告警把"首次部署"与"密钥丢失后重启"
	// 区分开: 新装看到属正常; 已有数据的实例看到必须立即从备份恢复密钥文件,
	// 旧凭据否则不可解且当前没有轮换/re-seal 工具可救。
	log.Warnf("seal: 加密密钥文件缺失, 已生成新密钥 %s(权限 0600)。首次部署属正常; 若本实例已有 nv1: 密文数据, 旧凭据将无法解密, 请立即从备份恢复密钥文件", keyFile)
	return raw[:], nil
}

func deriveKey(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}