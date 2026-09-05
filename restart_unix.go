//go:build !windows

package cosupdater

import (
	"fmt"
	"os"
	"syscall"
)

// restartProcess 在 Unix 上以新程序映像替换当前进程：
// 工作目录、环境变量与命令行参数自然继承；成功时 syscall.Exec 不返回。
// 可执行权限由 ApplyUpdate 收尾时的 chmod 0755 保证。
func restartProcess() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取当前程序路径失败: %w", err)
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		return fmt.Errorf("重启失败: %w", err)
	}
	return nil
}
