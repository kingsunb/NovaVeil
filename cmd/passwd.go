// passwd.go implements the NovaVeil `passwd` subcommand for offline password reset.
package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kingsunb/NovaVeil/internal/conf"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/seal"
	"github.com/spf13/cobra"
)

var passwdCmd = &cobra.Command{ // 设置现有用户密码的命令。
	Use:   "passwd [password]",
	Short: "Set user password (omit argument to read from stdin)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		password := ""
		if len(args) == 1 {
			password = args[0]
		} else {
			// 无参数时从 stdin 读入, 免进 shell 历史。
			line, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return fmt.Errorf("failed to read password from stdin: %w", readErr)
			}
			password = strings.TrimRight(line, "\r\n")
		}
		if err := op.ValidateNewPassword(password); err != nil {
			return err
		}
		if err := conf.Load(cfgFile); err != nil {
			return err
		}
		if err := db.InitDB(conf.AppConfig.Database.Type, conf.AppConfig.Database.Path, conf.IsDebug()); err != nil {
			return fmt.Errorf("database init error: %w", err)
		}
		defer db.Close()

		dataDir := dataDirectory()
		keyFile := filepath.Join(dataDir, "novaveil-encryption.key")
		if conf.AppConfig.Security.EncryptionKey == "" {
			if _, err := os.Stat(keyFile); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("encryption key file %s is missing; restore it before resetting the password", keyFile)
				}
				return fmt.Errorf("check encryption key file: %w", err)
			}
		}
		if err := seal.Configure(conf.AppConfig.Security.EncryptionKey, keyFile); err != nil {
			return fmt.Errorf("encryption init error: %w", err)
		}
		if err := op.UserResetPassword(password, dataDir); err != nil {
			return err
		}

		cmd.Println("Password updated. Restart the running server to apply the change.")
		return nil
	},
}

// init 注册密码设置命令及其配置文件参数。
func init() {
	passwdCmd.Flags().StringVar(&cfgFile, "config", "", "config file (default is ./data/config.json)")
	rootCommand.AddCommand(passwdCmd)
}
