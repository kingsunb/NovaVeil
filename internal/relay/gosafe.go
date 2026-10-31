package relay

import (
	"runtime/debug"

	"github.com/charmbracelet/log"
)

// goSafe 在独立协程运行 fn, panic 只记日志不击穿进程。
// 全部 fire-and-forget 派生点统一走此兜底。
func goSafe(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Errorf("relay %s panicked: %v\n%s", name, r, debug.Stack())
			}
		}()
		fn()
	}()
}
