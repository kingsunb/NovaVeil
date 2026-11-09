//go:build !windows

package cmd

import "github.com/spf13/cobra"

// runDesktop 非 Windows 平台无参数时保持帮助输出。
func runDesktop(cmd *cobra.Command) error {
	return cmd.Help()
}
