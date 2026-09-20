package update

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/log"
	"github.com/kingsunb/NovaVeil/internal/utils/shutdown"
)

// updateMu 串行化自更新: 更新含下载/校验/替换可执行文件并重启进程,
// 并发触发会在 os.Rename 替换环节互相竞争产生半成品状态。
var updateMu sync.Mutex

var ErrSelfUpdateDisabled = errors.New("自更新默认关闭，请设置 NOVAVEIL_ENABLE_SELF_UPDATE=true 或改用镜像升级")

func selfUpdateEnabled() bool {
	if v := os.Getenv("NOVAVEIL_DISABLE_SELF_UPDATE"); v == "1" || strings.EqualFold(v, "true") {
		return false
	}
	v := os.Getenv("NOVAVEIL_ENABLE_SELF_UPDATE")
	return v == "1" || strings.EqualFold(v, "true")
}

func UpdateCore() error {
	if !updateMu.TryLock() {
		return fmt.Errorf("已有更新任务正在进行中")
	}
	defer updateMu.Unlock()
	if !selfUpdateEnabled() {
		return ErrSelfUpdateDisabled
	}
	log.Infof("start update core")

	filename, err := getDownloadFilename()
	if err != nil {
		log.Warnf("update core failed: %v", err)
		return err
	}

	downloadUrl := updateUrl + "/" + filename
	log.Infof("download url: %s", downloadUrl)
	data, err := doRequestWithFallback(downloadUrl)
	if err != nil {
		log.Warnf("download failed: %v", err)
		return err
	}
	checksums, err := doRequestWithFallback(updateUrl + "/SHA256SUMS")
	if err != nil {
		return fmt.Errorf("下载校验和清单失败: %w", err)
	}
	if err := verifyReleaseChecksum(filename, data, checksums); err != nil {
		return err
	}

	execPath, err := os.Executable()
	if err != nil {
		log.Warnf("get executable path failed: %v", err)
		return err
	}
	execName := filepath.Base(execPath)

	tmpDir, err := os.MkdirTemp("", execName+"-update-*")
	if err != nil {
		log.Warnf("create temp dir failed: %v", err)
		return err
	}
	defer os.RemoveAll(tmpDir)
	log.Infof("using temp dir: %s", tmpDir)

	if err := unzip(data, tmpDir); err != nil {
		log.Warnf("unzip failed: %v", err)
		return err
	}

	newExec := filepath.Join(tmpDir, execName)
	if info, err := os.Stat(newExec); err != nil || info.IsDir() {
		log.Warnf("new executable not found at %s: %v", newExec, err)
		return fmt.Errorf("压缩包根目录未找到新版本可执行文件: %w", err)
	}
	log.Infof("new executable: %s", newExec)

	// 原子替换: 写入临时文件 → 验证 SHA256 → 保留 .old → 原子 rename 到最终路径。
	if err := replaceExecutable(newExec, execPath); err != nil {
		log.Warnf("replace executable failed: %v", err)
		return err
	}

	// 写入更新待验证标记: 新版本启动失败(崩溃)时, 下次启动自动回滚到 .old。
	if err := writeUpdateMarker(execPath); err != nil {
		log.Warnf("write update marker failed: %v", err)
		// 标记写入失败不阻断更新: 最坏情况是缺少自动回滚, 人工仍可从 .old 恢复。
	}

	log.Infof("update core success")
	go restartExecutable(execPath)
	return nil
}

func verifyReleaseChecksum(filename string, archive, manifest []byte) error {
	var expected string
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		name = strings.TrimPrefix(name, "./")
		if name == filename {
			expected = strings.ToLower(fields[0])
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("校验和清单中缺少 %s", filename)
	}
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("%s 的校验和格式非法", filename)
	}
	actualSum := sha256.Sum256(archive)
	actual := hex.EncodeToString(actualSum[:])
	if actual != expected {
		return fmt.Errorf("%s 的校验和不匹配", filename)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}

	_, err = io.Copy(out, in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

// replaceExecutable 原子替换可执行文件:
//  1. 将新二进制写入同目录临时文件(.new) — 同目录保证 rename 不跨文件系统
//  2. 校验临时文件 SHA256 与源文件一致 — 检测磁盘损坏/部分写入
//  3. 将当前二进制 rename 到 .old — 保留用于启动验证回滚
//  4. 原子 rename .new 到最终路径 — 安装新版本
//  5. 若步骤 4 失败, 回滚: rename .old 回最终路径
//
// .old 文件保留不删除, 供 CheckPendingUpdate 在新版本启动失败时自动回滚。
func replaceExecutable(newExec, execPath string) error {
	newPath := execPath + ".new"
	oldPath := oldExecPath(execPath)

	// 1. 写入临时文件(同目录, 保证 rename 原子性)。
	if err := copyFile(newExec, newPath); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}

	// 2. 验证完整性: 临时文件 SHA256 必须与源文件一致。
	if err := verifyFileIntegrity(newExec, newPath); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("完整性校验失败: %w", err)
	}

	// 3. 保存当前二进制用于回滚。
	_ = os.Remove(oldPath) // 清理上次更新遗留的 .old
	if err := os.Rename(execPath, oldPath); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("保存旧版本失败: %w", err)
	}

	// 4. 原子安装: rename 新二进制到最终路径。
	if err := os.Rename(newPath, execPath); err != nil {
		log.Errorf("原子安装失败, 尝试回滚: %v", err)
		if rbErr := os.Rename(oldPath, execPath); rbErr != nil {
			log.Errorf("回滚失败: %v", rbErr)
		}
		return fmt.Errorf("原子安装失败: %w", err)
	}

	// 5. 继承旧二进制的权限位。
	if info, statErr := os.Stat(oldPath); statErr == nil {
		_ = os.Chmod(execPath, info.Mode().Perm())
	}

	return nil
}

// verifyFileIntegrity 比较两个文件的 SHA256, 用于检测复制后的完整性。
func verifyFileIntegrity(src, dst string) error {
	srcSum, err := fileSHA256(src)
	if err != nil {
		return fmt.Errorf("计算源文件校验和: %w", err)
	}
	dstSum, err := fileSHA256(dst)
	if err != nil {
		return fmt.Errorf("计算目标文件校验和: %w", err)
	}
	if srcSum != dstSum {
		return fmt.Errorf("校验和不匹配: 源 %s, 目标 %s", srcSum, dstSum)
	}
	return nil
}

// fileSHA256 计算文件内容的 SHA256 十六进制摘要。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---------------------------------------------------------------------------
// 启动验证回滚
//
// 更新完成后写入 .update-pending 标记, 新版本启动时检查该标记:
//   - 标记存在且尝试次数未超限 → 递增计数, 继续启动; 启动成功后延迟清除标记
//   - 标记存在且尝试次数超限   → 回滚: rename .old 到最终路径, re-exec 旧版本
//   - 标记不存在               → 正常启动
//
// 这样, 新版本若在启动验证窗口内崩溃, 进程管理器(systemd/Docker)拉起后
// CheckPendingUpdate 会检测到残留标记并自动回滚到 .old。
// ---------------------------------------------------------------------------

const (
	// maxUpdateRetries 是回滚前允许的最大启动尝试次数。
	maxUpdateRetries = 3
	// updateVerificationDelay 是启动成功后清除标记前的等待时间。
	// 在此窗口内崩溃会被下次启动的 CheckPendingUpdate 检测到。
	updateVerificationDelay = 15 * time.Second
)

// oldExecPath 返回用于回滚的旧二进制路径。
func oldExecPath(execPath string) string {
	return execPath + ".old"
}

// updateMarkerPath 返回更新待验证标记文件路径。
func updateMarkerPath(execPath string) string {
	return execPath + ".update-pending"
}

// writeUpdateMarker 写入更新待验证标记, 计数从 1 开始。
func writeUpdateMarker(execPath string) error {
	return os.WriteFile(updateMarkerPath(execPath), []byte("1"), 0o600)
}

// CheckPendingUpdate 检查是否存在未完成的自更新验证标记。
// 若尝试次数超过 maxUpdateRetries, 自动回滚到 .old 并 re-exec(不返回)。
// 应在进程启动最早期(任何初始化之前)调用。
func CheckPendingUpdate() {
	execPath, err := os.Executable()
	if err != nil {
		return
	}
	markerPath := updateMarkerPath(execPath)
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return // 无标记, 正常启动
	}
	attempts, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if attempts < maxUpdateRetries {
		// 递增尝试计数, 继续启动新版本。
		_ = os.WriteFile(markerPath, []byte(strconv.Itoa(attempts+1)), 0o600)
		log.Warnf("更新验证进行中 (尝试 %d/%d)", attempts+1, maxUpdateRetries)
		return
	}

	// 超过重试上限: 回滚到 .old。
	oldPath := oldExecPath(execPath)
	if _, err := os.Stat(oldPath); err != nil {
		log.Errorf("回滚失败: 旧版本不存在 %s", oldPath)
		_ = os.Remove(markerPath)
		return
	}
	log.Warnf("更新在 %d 次尝试后仍未通过验证, 回滚到旧版本", attempts)
	if err := os.Rename(oldPath, execPath); err != nil {
		log.Errorf("回滚 rename 失败: %v", err)
		_ = os.Remove(markerPath)
		return
	}
	_ = os.Remove(markerPath)
	// 直接 re-exec 旧版本, 不走 shutdown(此时可能尚未初始化)。
	if runtime.GOOS == "windows" {
		// #nosec G702 -- 自更新回滚 re-exec：路径来自 os.Executable，参数为自身 os.Args，非外部输入。
		cmd := exec.Command(execPath, os.Args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			log.Errorf("回滚 re-exec 失败: %v", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	// #nosec G702 -- 自更新回滚 re-exec：路径来自 os.Executable，参数为自身 os.Args，非外部输入。
	if err := syscall.Exec(execPath, os.Args, os.Environ()); err != nil {
		log.Errorf("回滚 re-exec 失败: %v", err)
		os.Exit(1)
	}
}

// ClearUpdateMarker 在启动成功后延迟清除更新待验证标记。
// 在 updateVerificationDelay 窗口内崩溃会被下次启动检测到并回滚。
// 应在服务器成功监听后调用。
func ClearUpdateMarker() {
	execPath, err := os.Executable()
	if err != nil {
		return
	}
	markerPath := updateMarkerPath(execPath)
	if _, err := os.Stat(markerPath); err != nil {
		return // 无标记
	}
	go func() {
		time.Sleep(updateVerificationDelay)
		if err := os.Remove(markerPath); err == nil {
			log.Infof("更新验证通过, 已清除待验证标记")
		}
	}()
}

// getDownloadFilename 返回当前平台对应的发布归档名称。
func getDownloadFilename() (string, error) {
	arch := runtime.GOARCH
	goos := runtime.GOOS

	switch goos {
	case "windows":
		switch arch {
		case "amd64":
			return "novaveil-windows-amd64.zip", nil
		}
	case "darwin":
		switch arch {
		case "amd64":
			return "novaveil-darwin-amd64.zip", nil
		case "arm64":
			return "novaveil-darwin-arm64.zip", nil
		}
	case "linux":
		switch arch {
		case "386":
			return "novaveil-linux-386.zip", nil
		case "amd64":
			return "novaveil-linux-amd64.zip", nil
		case "arm":
			return "novaveil-linux-arm.zip", nil
		case "arm64":
			return "novaveil-linux-arm64.zip", nil
		}
	case "android":
		switch arch {
		case "386":
			return "novaveil-android-386.zip", nil
		case "amd64":
			return "novaveil-android-amd64.zip", nil
		case "arm":
			return "novaveil-android-arm.zip", nil
		case "arm64":
			return "novaveil-android-arm64.zip", nil
		}
	}
	return "", fmt.Errorf("不支持的平台: %s/%s", goos, arch)
}

func restartExecutable(execPath string) {
	shutdown.Shutdown()

	log.Infof("restarting: %q %q", execPath, os.Args[1:])

	if runtime.GOOS == "windows" {
		// #nosec G702 -- 自更新重启：路径来自 os.Executable，参数为自身 os.Args，非外部输入。
		cmd := exec.Command(execPath, os.Args[1:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			log.Errorf("restarting failed: %v", err)
			// Start 失败：进程无法启动，以非零退出码退出，让监控器感知失败并重试。
			os.Exit(1)
		}
		// 等待新进程启动完成，避免 cmd 进程成为孤儿。
		if err := cmd.Wait(); err != nil {
			log.Errorf("restarting failed after start: %v", err)
			os.Exit(1)
		}
		return
	}

	// #nosec G702 -- 自更新重启：路径来自 os.Executable，参数为自身 os.Args，非外部输入。
	if err := syscall.Exec(execPath, os.Args, os.Environ()); err != nil {
		log.Errorf("restarting failed: %v", err)
		// Exec 失败：以非零退出码退出，避免监控器误判为正常重启成功。
		os.Exit(1)
	}
}
