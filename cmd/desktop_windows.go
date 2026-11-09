//go:build windows

package cmd

import (
	_ "embed"
	"os"
	"os/signal"
	"syscall"

	"fyne.io/systray"
	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/conf"
	"github.com/kingsunb/NovaVeil/internal/utils/shutdown"
	"github.com/spf13/cobra"
	"golang.org/x/sys/windows"
)

//go:embed tray_icon.ico
var trayIcon []byte

// runDesktop 无参数启动: 起服务、开浏览器、驻留托盘, 退出走统一停机钩子。
func runDesktop(cmd *cobra.Command) error {
	if err := prepareStart(); err != nil {
		return err
	}
	if err := startService(); err != nil {
		return err
	}

	target := desktopURL(conf.AppConfig.Server.Host, conf.AppConfig.Server.Port)
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTooltip(conf.APP_NAME)

		openItem := systray.AddMenuItem("打开控制台", target)
		quitItem := systray.AddMenuItem("退出", "退出 "+conf.APP_NAME)
		go func() {
			for {
				select {
				case <-openItem.ClickedCh:
					openConsole(target)
				case <-quitItem.ClickedCh:
					log.Infof("收到托盘退出请求")
					shutdown.Shutdown()
					systray.Quit()
					return
				}
			}
		}()

		go func() {
			quit := make(chan os.Signal, 1)
			signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
			<-quit
			signal.Stop(quit)
			log.Infof("收到退出信号")
			shutdown.Shutdown()
			systray.Quit()
		}()

		openConsole(target)
	}, func() {})
	return nil
}

// openConsole 交给系统默认浏览器, 失败只提示不阻断服务。
func openConsole(target string) {
	if err := shellExecute(target); err != nil {
		log.Warnf("打开浏览器失败: %v, 请手动访问 %s", err, target)
		return
	}
	log.Infof("已在浏览器打开 %s", target)
}

// shellExecute 经 ShellExecuteW 交给默认协议处理器。
func shellExecute(target string) error {
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, nil, targetPtr, nil, nil, windows.SW_SHOWNORMAL)
}
