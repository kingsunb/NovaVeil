// user.go defines the NovaVeil user model and the request DTOs for login and
// credential changes, plus bcrypt password helpers.
package model

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// User is the persisted application user account.
type User struct {
	ID       uint   `gorm:"primaryKey"`
	Username string `gorm:"unique"`
	Password string `gorm:"not null"`
	// MustChangePassword 为 true 时(如首次初始化的随机密码)，登录后仅允许修改密码等白名单操作
	MustChangePassword bool
}

// UserStatus is the login-status payload returned to the frontend.
type UserStatus struct {
	// Username 供 web-next 探活后回填 Topbar 显示; 缺失时前端只能回落假 admin。
	Username           string `json:"username"`
	MustChangePassword bool   `json:"must_change_password"`
}

// UserLogin is the request body for the login endpoint.
type UserLogin struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// Remember 勾选「信任此设备」时为 true, 认证 cookie 持久 24 小时; 为 false 时签发
	// 会话 cookie(浏览器关闭即失效, 单次会话)。只能二选一, 不再接受客户端自报任意时长。
	Remember bool `json:"remember"`
}

// UserChangePassword is the request body for changing the current password.
type UserChangePassword struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// UserChangeUsername is the request body for renaming the account; Password
// confirms the current credentials.
type UserChangeUsername struct {
	NewUsername string `json:"new_username"`
	Password    string `json:"password"`
}

// HashPassword replaces u.Password with its bcrypt hash in place.
func (u *User) HashPassword() error {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}
	u.Password = string(hashedPassword)
	return nil
}

// ComparePassword reports nil when the plaintext matches the stored bcrypt hash.
func (u *User) ComparePassword(password string) error {
	return bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password))
}
