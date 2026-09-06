package cosupdater

// Restart 以指定程序路径重新启动；path 为空时用 os.Executable()。
//
// 自更新场景：更新被安装到带版本号的独立文件（见 NextExecutablePath），
// 应把 `path` 传成那次更新返回的新程序路径，而不是默认的 os.Executable()——
// 后者仍是旧的固定名程序。
//
// Windows：以一个分离的独立进程启动新程序，随即返回 nil；
// 调用方应尽快退出当前进程（如 os.Exit(0)），由新进程接管。
// Unix（Linux/macOS）：以 syscall.Exec 用新程序映像替换当前进程，
// 启动成功时本函数不返回。
//
// 两个平台均继承当前工作目录、命令行参数与环境变量——
// 以相对路径读取 config.json 的程序不受影响。
func Restart(path ...string) error {
	restartPath := ""
	if len(path) > 0 {
		restartPath = path[0]
	}
	return restartProcess(restartPath)
}
