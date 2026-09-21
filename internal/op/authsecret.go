package op

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// jwtSecretBytes JWT 签名密钥的随机字节数(HS256 要求至少 32 字节)
const jwtSecretBytes = 32

var (
	jwtSecretMu sync.Mutex
)

// AuthJWTSecretGet 返回 JWT 签名密钥; 为空时生成 32 字节随机密钥并持久化到 settings KV
func AuthJWTSecretGet() (string, error) {
	jwtSecretMu.Lock()
	defer jwtSecretMu.Unlock()

	// 走内部 helper, 跳过 SettingGetString 对外的 JWT 密钥读取守卫, 防止
	// 自身签名路径被自己拦截。
	if secret, err := settingGetInternal(model.SettingKeyAuthJWTSecret); err == nil && secret != "" {
		return secret, nil
	}
	secret, err := randomHexSecret(jwtSecretBytes)
	if err != nil {
		return "", fmt.Errorf("生成 JWT 密钥失败: %w", err)
	}
	if err := authJWTSecretSave(secret); err != nil {
		return "", err
	}
	return secret, nil
}

// AuthJWTRotateSecret 轮换 JWT 签名密钥, 使所有已签发 token 失效
func AuthJWTRotateSecret() error {
	jwtSecretMu.Lock()
	defer jwtSecretMu.Unlock()

	secret, err := randomHexSecret(jwtSecretBytes)
	if err != nil {
		return fmt.Errorf("生成 JWT 密钥失败: %w", err)
	}
	return authJWTSecretSave(secret)
}

// authJWTSecretSave 将新密钥写入 settings 表并更新缓存, 供 AuthJWTSecretGet /
// AuthJWTRotateSecret 等独立轮换路径使用(不在事务内)。
func authJWTSecretSave(secret string) error {
	if err := authJWTSecretSaveTx(db.GetDB(), secret); err != nil {
		return err
	}
	settingCache.Set(model.SettingKeyAuthJWTSecret, secret)
	return nil
}

// authJWTSecretSaveTx 仅将新密钥写入 settings 表, 不触碰缓存, 供事务内调用: 由调用方
// 在事务提交成功后再发布缓存, 保证缓存不领先于 DB, 事务回滚时也不留下脏缓存。
func authJWTSecretSaveTx(tx *gorm.DB, secret string) error {
	stored, err := sealSettingValue(model.SettingKeyAuthJWTSecret, secret)
	if err != nil {
		return err
	}
	result := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&model.Setting{Key: model.SettingKeyAuthJWTSecret, Value: stored})
	if result.Error != nil {
		return fmt.Errorf("保存 JWT 密钥失败: %w", result.Error)
	}
	return nil
}

func randomHexSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("读取随机字节失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}
