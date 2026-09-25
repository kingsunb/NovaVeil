package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/builtin"
	"github.com/kingsunb/NovaVeil/internal/conf"
	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/eval"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/relay"
	"github.com/kingsunb/NovaVeil/internal/seal"
	"github.com/kingsunb/NovaVeil/internal/server"
	"github.com/kingsunb/NovaVeil/internal/task"
	"github.com/kingsunb/NovaVeil/internal/utils/shutdown"
	"github.com/spf13/cobra"
)

var cfgFile string

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start " + conf.APP_NAME,
	// PreRunE 而非 PreRun: cobra 只有 PreRunE 返回错误才会跳过 Run,
	// PreRun 里 return 仍会带着零值配置继续执行。
	PreRunE: func(cmd *cobra.Command, args []string) error {
		conf.PrintBanner()
		// 配置加载失败(格式非法等)必须中止启动: 静默回退默认值会让服务以错误的
		// 端口/数据库路径起起来, 排查成本远高于显式失败。
		if err := conf.Load(cfgFile); err != nil {
			return err
		}
		if level, err := log.ParseLevel(conf.AppConfig.Log.Level); err == nil {
			log.SetLevel(level)
		}
		return nil
	},
	// RunE 而非 Run: 启动任一环节失败必须把错误交回 cobra → Execute → os.Exit(1),
	// 否则进程以退出码 0 结束, Docker restart: on-failure 与 systemd Restart=on-failure 都不会拉起。
	RunE: func(cmd *cobra.Command, args []string) error {
		shutdown.Init(log.Default())
		if err := db.InitDB(conf.AppConfig.Database.Type, conf.AppConfig.Database.Path, conf.IsDebug()); err != nil {
			log.Errorf("database init error: %v", err)
			return fmt.Errorf("数据库初始化失败: %w", err)
		}
		// 数据库 Close 必须排他：作为最终器在所有常规钩子全部结束
		// （包括等待超时的钩子 goroutine 归零）之后同步执行。
		shutdown.RegisterFinalizer(db.Close)

		// 静态加密必须先于任何业务读写初始化: builtin 补建渠道、op.InitCache 刷新
		// 缓存都会接触敏感字段, 这里未 Configure 时 seal 会回退到进程临时密钥,
		// 本次写入的密文重启后无法解密。dataDir 提前至此, 与后续会话/初始密码
		// 文件的目录派生保持同源。
		dataDir := dataDirectory()
		if err := seal.Configure(conf.AppConfig.Security.EncryptionKey, filepath.Join(dataDir, "novaveil-encryption.key")); err != nil {
			log.Errorf("encryption init error: %v", err)
			return fmt.Errorf("加密初始化失败: %w", err)
		}

		// 内置渠道（免费 + 官方）在缓存加载前补建，保证启动后即出现在渠道列表。
		// 免费渠道出厂启用、内置 Key；官方渠道出厂禁用、无 Key，管理员配置后参与路由。
		if err := builtin.EnsureBuiltinChannels(context.Background()); err != nil {
			log.Errorf("builtin channels init error: %v", err)
			return fmt.Errorf("内置渠道初始化失败: %w", err)
		}

		if err := op.InitCache(); err != nil {
			log.Errorf("cache init error: %v", err)
			return fmt.Errorf("缓存初始化失败: %w", err)
		}
		// 重建缓存后, 把已经不在数据库里的 channel ID 从 relay 状态中清掉:
		// 否则用同一 ID 重建的渠道会带旧 401 冷却 / RPM 窗口 / 并发槽位, 污染新渠道。
		// 依赖放在 cmd 层而不是 op 内, 避免 op ↔ relay 循环依赖。
		for _, ch := range op.ChannelList() {
			relay.CleanupChannelKeyState(ch.ID)
		}
		op.InitConversationStore(dataDir)

		if err := op.UserInit(dataDir); err != nil {
			log.Errorf("user init error: %v", err)
			return fmt.Errorf("管理员初始化失败: %w", err)
		}

		eval.Default = eval.NewScheduler()
		if err := eval.Default.Start(context.Background()); err != nil {
			log.Errorf("eval scheduler start error: %v", err)
			return fmt.Errorf("评估调度器启动失败: %w", err)
		}

		if err := server.Start(); err != nil {
			log.Errorf("server start error: %v", err)
			return fmt.Errorf("服务启动失败: %w", err)
		}
		// Register in reverse execution order. Shutdown executes strict LIFO:
		// 1) cancel in-flight HTTP contexts + gracefully drain/force-close ingress,
		// 2) cancel task lifecycle + wait for background task goroutines,
		// 3) flush in-memory writers, 4) close the database last.
		// Each hook also has its own context deadline in addition to the shutdown
		// package's outer timeout.
		shutdown.Register(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			return op.FlushErrorLogQueue(ctx)
		})
		shutdown.Register(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			return op.ClientStatFlush(ctx)
		})
		shutdown.Register(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			return op.FlushConversations(ctx)
		})
		shutdown.Register(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			return op.APIKeyTouchLastUsedFlush(ctx)
		})
		// 任务泵与评估 worker 允许在停机时处理较长的收尾(如跑完当前 10 分钟 eval),
		// 但不能无限悬挂: 给 5 分钟有界超时。注册顺序上 server shutdown 在它们之后,
		// LIFO 执行时先停 ingress, 再停 eval, 再停任务, 最后才到 DB close。
		shutdown.RegisterWithTimeout(task.StopAll, 5*time.Minute)
		shutdown.RegisterWithTimeout(func() error {
			if eval.Default != nil {
				return eval.Default.Stop()
			}
			return nil
		}, 5*time.Minute)
		shutdown.Register(func() error {
			// 先取消所有活动 HTTP 请求的根 context, 让在途 handler 感知停机并尽快收尾;
			// 再以有界超时 drain 等待 handler 终态, 超时则 server.Shutdown 内部调用
			// Close 强制中断残余连接。这保证 flush/DB close 阶段不会有 handler 继续写入。
			server.CancelInFlight()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			return server.Shutdown(ctx)
		})

		task.Init()
		go task.RUN()
		shutdown.Listen()
		return nil
	},
}

// dataDirectory 派生一次性管理员密码文件与会话归档所在的数据目录。
// 仅 SQLite 的 Database.Path 是文件路径, 取其父目录; MySQL/PG 的 Path 是 DSN,
// 当目录用会在 Windows 上因非法字符启动失败、Linux 上把 DSN(含数据库密码)嵌进目录名,
// 因此非 SQLite 一律回退默认的 data 目录; SQLite 路径无目录成分时同样回退。
func dataDirectory() string {
	if t := conf.AppConfig.Database.Type; t == "sqlite" || t == "" {
		if dir := filepath.Dir(conf.AppConfig.Database.Path); dir != "" && dir != "." {
			return dir
		}
	}
	return "data"
}

func init() {
	startCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./data/config.json)")
	rootCommand.AddCommand(startCmd)
}
