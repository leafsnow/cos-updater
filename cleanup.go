package cosupdater

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/go-version"
)

// CleanupOldVersions 清理与 currentPath 同目录、同名基础、带 "_v<版本>" 命名后缀的旧程序
// 版本文件，只保留版本最高的 keep 个，其余删除。用于更新后让每个 `_v{ver}.exe` 历史版本
// 不至于无限堆积。
//
// 规则：
//   - currentPath 自身，以及不带版本号的固定名文件（如 "xxx.exe"）一律不动；
//   - 只删除文件名匹配 `<基础名>_v<semver><扩展名>` 且语义版本可解析的兄弟文件；
//   - 按版本号从高到低排序，删除排名在 keep 之后（即更旧的）文件；
//   - keep 建议用 2：保留当前最新版 + 上一个版本，可回滚且目录不膨胀。
func CleanupOldVersions(currentPath string, keep int) error {
	if keep < 1 {
		keep = 1
	}
	currentPath, err := filepath.Abs(currentPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(currentPath)
	base := filepath.Base(currentPath)
	ext := filepath.Ext(base)
	// 程序基础名：当前运行文件可能本身带版本号（如 app_v1.0.2.exe），
	// 需剥掉 "_v<版本>" 后缀才是可匹配所有历史版本的 "app"。
	stem := strings.TrimSuffix(base, ext)
	baseName := stem
	if i := strings.LastIndex(stem, "_v"); i >= 0 {
		if _, err := version.NewVersion(stem[i+2:]); err == nil {
			baseName = stem[:i]
		}
	}

	items, err := filepath.Glob(filepath.Join(dir, baseName+"_v*"+ext))
	if err != nil {
		return err
	}

	type item struct {
		path, ver string
	}
	var found []item
	for _, p := range items {
		abs, _ := filepath.Abs(p)
		ver := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), baseName+"_v"), ext)
		if _, err := version.NewVersion(ver); err != nil {
			continue // 非语义版本命名（如用户自己的备份），跳过
		}
		found = append(found, item{abs, ver})
	}
	if len(found) <= keep {
		return nil
	}

	// 按版本从高到低排序，保留「最新 keep 个」，删除更旧的。
	sort.Slice(found, func(i, j int) bool {
		vi, _ := version.NewVersion(found[i].ver)
		vj, _ := version.NewVersion(found[j].ver)
		return vi.GreaterThan(vj) // 版本高的在前
	})

	for _, f := range found[keep:] {
		if f.path == currentPath {
			continue // 防御：无论如何不删自己
		}
		if err := os.Remove(f.path); err != nil {
			return fmt.Errorf("删除旧版本 %s 失败: %w", f.path, err)
		}
	}
	return nil
}

// Retire 把正在运行的旧程序（path，通常取 os.Executable()）改名为同目录下的
// `<原名>.old-<时间戳>`：文件仍可见、可回滚、不能被双击运行，且不加隐藏属性。
// 时间戳精确到秒，配合冲突递增后缀，保证多次退役生成的备份名彼此唯一、互不覆盖，
// 从而保留多份历史版本（连续更新可回滚到任意一版）。
//
// Windows 允许重命名正在运行的 exe（只是不能覆盖/删除它），因此可在当前进程退出前调用。
// 顺序：退役旧程序 -> 启动新版本（Restart）-> 退出当前进程。
//
// 规则：
//   - 始终生成新名（不与已有备份冲突），不删除任何已有备份；
//   - 返回退役后的新路径；失败返回 error，调用方可选择忽略——退役只是「可回滚标记」，
//     不阻断更新主流程（新版本已安装到固定名上）。
func Retire(path string) (string, error) {
	if path == "" {
		var err error
		path, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("获取当前程序路径失败: %w", err)
		}
	}
	stamp := time.Now().Format("20060102-150405")
	old := fmt.Sprintf("%s.old-%s", path, stamp)
	for i := 1; ; i++ {
		if _, err := os.Stat(old); os.IsNotExist(err) {
			break
		}
		old = fmt.Sprintf("%s.old-%s_%d", path, stamp, i)
	}
	if err := os.Rename(path, old); err != nil {
		return "", fmt.Errorf("退役旧版本（改名 .old）失败: %w", err)
	}
	return old, nil
}
