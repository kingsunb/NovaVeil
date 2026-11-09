// root.go defines the NovaVeil root CLI command.
package cmd

import (
	"os"

	"github.com/kingsunb/NovaVeil/internal/conf"
	"github.com/spf13/cobra"
)

var rootCommand = &cobra.Command{
	Use:   conf.APP_NAME,
	Short: conf.APP_DESC,
	// 无参数时 Windows 进入桌面模式, 其余平台保持帮助输出。
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return cmd.Help()
		}
		return runDesktop(cmd)
	},
}

// Execute runs the root command and exits with status 1 on error.
func Execute() {
	err := rootCommand.Execute()
	if err != nil {
		os.Exit(1)
	}
}
