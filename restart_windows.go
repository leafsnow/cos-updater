//go:build windows

package cosupdater

import (
	"fmt"
	"os"
	"syscall"
)

// restartProcess 在 Windows 上直接启动新 exe。
//
// 旧实现是 `cmd /c ping ... & start "" "<exe>"`，在含中文/空格的路径上，
// cmd 对引号与路径的二次解析会错乱，可能把目标解析成只剩反斜杠的"路径"，
// 表现为 Windows 弹出「找不到 '\\' 文件」。改用 os.StartProcess（CreateProcess）
// 直接启动：路径原样交给系统，无 cmd/start 解释器介入，中文、空格、长路径均正确。
//
// CREATE_NEW_PROCESS_GROUP|DETACHED_PROCESS 让新进程独立于本进程组与控制台——
// 本进程（运行中的老程序，已被 Retire 改名成 .old）随即 os.Exit(0)，不影响新进程。
// path 为空时回退到 os.Executable()。
func restartProcess(path string) error {
	exe := path
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return fmt.Errorf("获取当前程序路径失败: %w", err)
		}
	}
	attr := &os.ProcAttr{
		Files: []*os.File{nil, nil, nil}, // 不继承父进程标准句柄
		Sys: &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
		},
	}
	proc, err := os.StartProcess(exe, []string{exe}, attr)
	if err != nil {
		return fmt.Errorf("启动新版本失败: %w", err)
	}
	// 及时释放句柄：父进程即将退出，无需持有对新进程的引用，避免句柄遗留。
	_ = proc.Release()
	return nil
}
