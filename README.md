# cos-updater

基于腾讯云 COS 的极简 Go 程序自更新库：检查新版本 → 下载 → SHA256 校验 → 原子替换当前程序。
为 SyncLedger 等本地项目而建，亦可用于任何 Go 程序。

## 特性

- **配置极简**：桶地址 + 版本文件路径 + 当前版本号，三行配置即可工作
- **零 SDK 依赖**：私有读桶通过手写 COS XML API 签名实现，全库仅依赖 go-version 与 go-update 两个小库
- **进度回调**：每 64KB 触发一次，收尾补发保证进度条能到 100%
- **强制校验和**：version.json 缺 checksum 拒绝下载，杜绝「更新后变砖」
- **原子替换**：基于 go-update 的 rename 机制，Windows 下替换运行中的 exe 不崩溃
- **可取消**：所有 API 接受 context.Context，GUI 的取消按钮就是一次 cancel()
- **多平台**：version.json 内建 platforms 映射，按 GOOS/GOARCH 自动选择产物
- **一键重启**：内置 Restart()，处理 Windows 中文文件名、等待旧进程退出与 cwd 继承

## 安装

```bash
go get github.com/leafsnow/cos-updater
```

本地联调时可临时用 replace 指向本目录（联调结束后删除该行即可）：

```
# SyncLedger/go.mod
require github.com/leafsnow/cos-updater v1.0.0

replace github.com/leafsnow/cos-updater => ../GoUpdate
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
		BucketURL:       "https://bucket-1250000000.cos.ap-guangzhou.myqcloud.com",
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
	if err := cosupdater.ApplyUpdate(context.Background(), cfg, info); err != nil {
		log.Printf("更新失败: %v", err)
		return
	}

	log.Println("更新成功，重启生效")
	cosupdater.Restart()
	os.Exit(0)
}
```

## version.json 规范

放在桶内 Config.VersionFilePath 处。platforms 必需，key 为 GOOS/GOARCH；
每个平台条目必须含 download_url（桶内相对路径，或绝对 URL——绝对 URL 仅公有读模式允许）
与 checksum（强制，格式 sha256:<64位十六进制>）。

```json
{
  "version": "2.0.0",
  "release_notes": "修复了 6 月账单导出的合计行错误",
  "platforms": {
    "windows/amd64": {
      "download_url": "updates/2.0.0/合洋泰拼多多对账分析系统.exe",
      "checksum": "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
    },
    "linux/amd64": {
      "download_url": "updates/2.0.0/syncledger-linux",
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

## API 一览

| 函数 | 说明 |
|---|---|
| `CheckUpdate(ctx, cfg) (has bool, info *VersionInfo, err error)` | 检查是否有新版本；版本相同或更低返回 has=false，info 仍携带解析结果可供展示 |
| `ApplyUpdate(ctx, cfg, info) error` | 下载 → 强制 SHA256 校验 → 原子替换。失败时原文件保留、临时文件清理 |
| `RunUpdate(ctx, cfg) error` | CheckUpdate + ApplyUpdate 的静默组合；无新版本时返回 nil |
| `Restart() error` | 可选。以新程序重启让更新生效；Windows 安排延迟启动后返回，Unix 替换进程映像且不返回 |
| `info.AssetForPlatform(goos, arch)` | 预检 platforms 中是否有指定平台的产物，便于更新前预判 |

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

## 设计决策（与需求文档 v1.0.0 的差异，均已与作者确认）

- **ctx 首参**：文档原签名无取消能力，且其「内置 30s 总超时」会掐断慢网络下的大文件下载。
  改为：版本检查 30s 超时；下载不限总时长，取消与超时交给调用方的 ctx。
- **手写 COS 签名，零 SDK 依赖**：依赖树只有 go-version 与 go-update。
  签名窗口 15 分钟；若签名出错会得到 403，极易定位。
- **platforms 必需**：单平台发布也要写 platforms 映射，不提供顶层简写形态。
- **强制 checksum**：缺 checksum 直接报 ErrChecksumRequired，不执行下载。
- **Restart() 可选**：Windows 用 ping 延迟约 1 秒 + start 启动（避开 GUI 进程中 timeout 命令
  依赖 stdin 的限制），处理中文文件名并继承工作目录；Unix 用 syscall.Exec 替换进程映像。
- go-update 要求目标文件已存在，库会先做存在性检查并给出可读错误。

## COS 桶准备

- 公有读：直接使用，无需任何密钥。完整性由 HTTPS 与强制 SHA256 兜底，
  这是分发自家 exe 场景的业界标准做法。
- 私有读：提供 SecretID 与 SecretKey，库为每次请求生成 15 分钟有效的签名 URL。
  注意：私有读模式下版本文件同样需要下载权限。

## 发布新版本流程

1. 构建：`./build.sh`（Fyne GUI 记得 `-H windowsgui` 与 `-ldflags "-X main.version=..."`）
2. 计算校验和：sha256sum 或 PowerShell Get-FileHash
3. 上传产物到桶，如 `updates/2.0.0/合洋泰拼多多对账分析系统.exe`（按版本子目录，避免同名覆盖）
4. 生成并上传 version.json（platforms 中每个平台一条）。
   顺序很重要：**必须先传产物、后传 version.json**——JSON 里写着产物的校验和，
   先传 JSON 会让旧版本客户端拿到指向 404 的下载地址

## 测试

```bash
go test ./... -count=1
```

48 个自动化测试全部通过，覆盖：版本比较、校验和解析、URL 拼接与转义、
COS 签名 URL 形态、平台选择、完整更新流程（公有读/私有读）、
校验失败回滚、ctx 取消下载、进度回调粒度与 100% 补发。

Windows 手动验证清单（自动化测试无法覆盖的环节）：

1. 替换正在运行的 exe：自更新 + Restart() 后确认无崩溃且新版本生效
2. 私有读桶实测：签名 URL 对真实 COS 服务的兼容性无法用 httptest 覆盖，
   发布前务必用真实私有桶验证一次；403 = 签名算法/密钥/时钟偏差问题
3. 大文件进度回调：观察 OnProgress 触发频率与最终 100% 补发
4. Unix 权限：Linux/macOS 上更新后确认可执行权限保持 0755

## 许可证

TODO
