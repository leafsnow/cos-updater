# cos-updater

基于腾讯云 COS 的极简 Go 程序自更新库：检查新版本 → 下载 → SHA256 校验 → 退役旧程序（改名 `.old`）→ 落位**固定名**新程序 → 关闭旧程序并自动启动新版本。
为 SyncLedger 等本地项目而建，亦可用于任何 Go 程序。

## 核心原则：固定名入口，程序名永远不变

- 无论怎么升级，本地程序文件名始终是**固定名**（如 `合洋泰销售提成分析工具.exe`），COS 产物同名、不带版本号。
- 磁盘上只保留两种文件：**当前新版**（固定名）+ **历史备份**（`固定名.old-<时间戳>`）。
  每次更新都把旧固定名改名为 `.old-<时间戳>`（保留多份、可见、可回滚、双击不可运行、不加隐藏属性），再落位新版本到固定名。
- 连续更新不会产生 `工具_v1.0.6_v1.0.7.exe` 这类嵌套名：`NextExecutablePath` 会剥离基准名里的 `_v<版本>` 后缀。
- Windows 上运行中的 exe 只能重命名、不能覆盖/删除，因此「先退役旧固定名 → 再落位新固定名」的顺序由库内部保证，调用方无需关心。

## 安装

```bash
go get github.com/leafsnow/cos-updater
```

## 快速开始（GUI 场景）

```go
package main

import (
	"context"
	"log"
	"os"

	cosupdater "github.com/leafsnow/cos-updater"
)

var version = "v1.0.0" // 构建时以 -ldflags "-X main.version=xxx" 注入

func main() {
	cfg := &cosupdater.Config{
		BucketURL:       "",
		VersionFilePath: "updates/version.json",
		CurrentVersion:  version,
		// 私有读桶才需要；公有读桶两项都留空即可
		SecretID:  os.Getenv("COS_SECRET_ID"),
		SecretKey: os.Getenv("COS_SECRET_KEY"),
		OnProgress: func(downloaded, total int64) {
			// 回调在下载 goroutine 中触发，GUI 须自行切回 UI 线程（如 fyne.Do(...)）
			log.Printf("下载 %d / %d bytes", downloaded, total)
		},
	}

	has, info, err := cosupdater.CheckUpdate(context.Background(), cfg)
	if err != nil {
		log.Printf("检查更新失败: %v", err)
		return
	}
	if !has {
		return // 已是最新版本
	}

	// 弹窗展示 info.Version 与 info.ReleaseNotes，用户确认后执行更新
	newPath, err := cosupdater.ApplyUpdate(context.Background(), cfg, info)
	if err != nil {
		log.Printf("更新失败: %v", err)
		return
	}

	// ApplyUpdate 已在库内自动完成「退役旧程序(.old) → 落位固定名」，此处直接启动新版本并退出旧进程。
	log.Printf("更新成功，启动新版 %s", newPath)
	cosupdater.Restart(newPath) // 启动固定名新程序
	os.Exit(0)                  // 退出当前(旧)进程
}
```

## version.json 规范

放在桶内 `{Prefix}/version.json`（Prefix 即该程序在桶根下的子目录名）。
仓库附有可直接复制改用的示例：[version.example.json](version.example.json)。

platforms 必需，key 为 GOOS/GOARCH；每个平台条目必须含 download_url（**相对桶根的路径**，
含 Prefix；或绝对 URL——绝对 URL 仅公有读模式允许）与 checksum（强制，格式 sha256:<64位十六进制>）。
`download_url` 指向的产物文件名即程序固定名（不带版本号）。

```json
{
  "version": "2.0.0",
  "release_notes": "修复了 6 月账单导出的合计行错误",
  "platforms": {
    "windows/amd64": {
      "download_url": "AutoPddTax/2.0.0/windows/amd64/AutoPddTax.exe",
      "checksum": "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
    },
    "linux/amd64": {
      "download_url": "AutoPddTax/2.0.0/linux/amd64/AutoPddTax",
      "checksum": "sha256:fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321"
    }
  }
}
```

生成 checksum：

```bash
sha256sum dist/xxx.exe                      # Linux/macOS
Get-FileHash .\xxx.exe -Algorithm SHA256   # Windows PowerShell
```

> 手动维护时通常不用手算 checksum：直接用库内的发布命令，它会自动计算并写入。

## API 一览

| 函数 | 说明 |
|---|---|
| `CheckUpdate(ctx, cfg) (has bool, info *VersionInfo, err error)` | 检查是否有新版本；版本相同或更低返回 has=false，info 仍携带解析结果可供展示 |
| `ApplyUpdate(ctx, cfg, info) (newPath string, err error)` | 下载 → 强制 SHA256 校验 → 退役旧运行程序（改名为 `<固定名>.old-<时间戳>`，保留多份备份）→ 新版本落位到**固定名** → 返回固定名路径。退役/落位顺序由库内保证，调用方无须关心 Windows 约束；失败时原程序保留、临时文件清理 |
| `RunUpdate(ctx, cfg) error` | CheckUpdate + ApplyUpdate 的静默组合（丢弃返回路径）；无新版本时返回 nil |
| `NextExecutablePath(cfg, info)` | 计算新版将要安装到的**固定名**路径（剥掉基准名里的 `_v<版本>` 后缀，程序名永不变、连续更新不嵌套），供确认弹窗展示 |
| `CleanupOldVersions(currentPath, keep)` | 清理同目录「带 `_v<版本>` 后缀」的历史文件。固定名模式下磁盘不再积累这类文件，此函数基本用不到，保留用于兼容旧版本 |
| `Retire(path string) (oldPath string, err error)` | 把正在运行的旧程序改名为同目录 `<原名>.old-<时间戳>`：文件名唯一（同名冲突自动递增后缀）、保留多份、可见、可回滚、双击不再运行、不加隐藏属性。已由 `ApplyUpdate` 内落位前自动调用；公开可单独复用 |
| `Restart(path ...string) error` | 重启让更新生效。传新版本路径；为空时用 os.Executable()。Windows 安排延迟启动后返回，Unix 替换进程映像且不返回 |
| `info.AssetForPlatform(goos, arch)` | 预检 platforms 中是否有指定平台的产物，便于更新前预判 |
| `Publish(ctx, PublishConfig, []PublishAsset) error` | 发布端：上传产物 + 自动算校验和 + 生成上传 version.json。**先产物后 version.json**，避免旧客户端拉到 404 |

### 错误 sentinel（配合 errors.Is 使用）

| 错误 | 含义 |
|---|---|
| `ErrInvalidConfig` | 配置缺失或非法（如 BucketURL 无 scheme、必填项为空） |
| `ErrSecretPair` | SecretID 与 SecretKey 只提供了其中一个 |
| `ErrInvalidVersionInfo` | 版本文件 JSON 缺字段或结构不符约定 |
| `ErrPlatformNotSupported` | platforms 中没有当前平台（GOOS/GOARCH）条目 |
| `ErrChecksumRequired` | 缺 checksum 或格式非 sha256:hex；本库强制校验，缺省即拒绝下载 |
| `ErrChecksumMismatch` | 下载内容与 checksum 不符。临时文件已清理，原文件未动 |
| `ErrInvalidDownloadURL` | 私有读模式下 download_url 却是绝对 URL |

## COS 桶准备

- **多程序一个桶**：桶根地址全局唯一（如 ``），
  桶下按程序各放一个子目录，目录名即该程序的 Prefix（如 `AutoPddTax`）。新程序接入只加一个新前缀。
- **客户端读取（公有读）**：直接使用，无需任何密钥，完整性由 HTTPS 与强制 SHA256 兜底。
- **客户端读取（私有读）**：提供 SecretID 与 SecretKey，库为每次请求生成 15 分钟有效的签名 URL。
- **发布上传（私有写）**：需要 SecretID 与 SecretKey，仅发布端（build.sh）使用，**客户端一律不需要**。

## 发布新版本

用内置的通用发布命令，一次完成「上传产物 + 自动算校验和 + 生成上传 version.json」：

```bash
cosup-publish \
  -bucket https://xxxxx.com \
  -prefix AutoPddTax -version 2.0.0 \
  -secret-id $COS_SECRET_ID -secret-key $COS_SECRET_KEY \
  -asset "windows/amd64=dist/AutoPddTax.exe" \
  -note "发行说明"
```

- `-asset` 可多次（每个平台一条）；`-bucket` 与密钥缺省时回退到环境变量
  `COS_BUCKET` / `COS_SECRET_ID` / `COS_SECRET_KEY`，避免密码进命令行历史。
- 产物上传到 `{Prefix}/{Version}/{GOOS}/{GOARCH}/{文件名}`（版本 + 平台双层子目录，
  防多平台同名覆盖）；加 `-flat` 则改为 `{Prefix}/{GOOS}/{GOARCH}/{文件名}`（去版本段，
  桶只留最新，不保留历史）。version.json 固定上传到 `{Prefix}/version.json`。
- 顺序自动处理：**先全部产物、后 version.json**，旧客户端不会拿到指向 404 的地址。

### Publish 库 API（把发布逻辑内嵌进自有工具时用）

```go
type PublishAsset struct {
	GOOS, GOARCH, FilePath string      // 如 {"windows","amd64","dist/app.exe"}
}
type PublishConfig struct {
	BucketURL, Prefix, Version string  // Prefix = 程序在桶下的子目录名
	FlatLayout bool                   // true: 产物路径不带版本子目录
	ReleaseNotes, SecretID, SecretKey string
}
err := cosupdater.Publish(ctx, cfg, []cosupdater.PublishAsset{{GOOS: "windows", GOARCH: "amd64", FilePath: "dist/app.exe"}})
```
