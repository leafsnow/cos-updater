//go:build windows

package cosupdater

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// restartProcess 在 Windows 上安排延迟启动新 exe：
// 用 ping 空转约 1-2 秒等待当前进程退出（timeout 命令在无控制台的 GUI 进程中
// 会因 stdin 重定向而报错，ping 无此问题），随后 start 启动新程序。
// path 为空时回退到 os.Executable()。start 的第一个空参数 "" 是窗口标题占位，
// 保证含空格/中文的 exe 路径被整体引用。
func restartProcess(path string) error {
	exe := path
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return fmt.Errorf("获取当前程序路径失败: %w", err)
		}
	}
	script := fmt.Sprintf(`ping -n 2 127.0.0.1 >nul & start "" "%s"`, exe)
	cmd := exec.Command("cmd", "/c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("安排重启失败: %w", err)
	}
	go cmd.Wait() // 异步回收子进程资源
	return nil
}
