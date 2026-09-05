package cosupdater

// Restart 以当前可执行文件路径重新启动程序，让更新后的新版本生效。
//
// Windows：安排一个延迟约 1 秒的分离进程来启动新 exe，随即返回 nil；
// 调用方应尽快保存数据并退出当前进程（如 os.Exit(0)）。
// Unix（Linux/macOS）：以 syscall.Exec 用新程序映像替换当前进程，
// 启动成功时本函数不返回。
//
// 两个平台均继承当前工作目录、命令行参数与环境变量——
// 以相对路径读取 config.json 的程序（如 SyncLedger）不受影响。
// 若想指定路径重启，请自行调用平台实现；本函数始终重启 os.Executable()。
func Restart() error {
	return restartProcess()
}
