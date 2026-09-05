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
- **发布工具**：内置 Publish API 与通用发布子命令，上传产物、自动计算校验和、生成并上传 version.json
  —— 配合 build.sh 一句命令完成「构建 + 自动发布新版本」
- **一桶多程序**：桶根地址全局唯一，每个程序只用一个 Prefix（桶下子目录名）区分，新程序接入近乎零配置

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

放在桶内 `{Prefix}/version.json`（Prefix 即该程序在桶根下的子目录名）。
仓库附有可直接复制改用的示例：[version.example.json](version.example.json)。

platforms 必需，key 为 GOOS/GOARCH；每个平台条目必须含 download_url（**相对桶根的路径**，
含 Prefix；或绝对 URL——绝对 URL 仅公有读模式允许）与 checksum（强制，格式 sha256:<64位十六进制>）。

```json
{
  "version": "2.0.0",
  "release_notes": "修复了 6 月账单导出的合计行错误",
  "platforms": {
    "windows/amd64": {
      "download_url": "AutoPddTax/2.0.0/windows/amd64/拼多多商店自动开票系统.exe",
      "checksum": "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
    },
    "linux/amd64": {
      "download_url": "AutoPddTax/2.0.0/linux/amd64/syncledger-linux",
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
| `ApplyUpdate(ctx, cfg, info) error` | 下载 → 强制 SHA256 校验 → 原子替换。失败时原文件保留、临时文件清理 |
| `RunUpdate(ctx, cfg) error` | CheckUpdate + ApplyUpdate 的静默组合；无新版本时返回 nil |
| `Restart() error` | 可选。以新程序重启让更新生效；Windows 安排延迟启动后返回，Unix 替换进程映像且不返回 |
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

- **多程序一个桶**：桶根地址全局唯一（如 `https://bucket-appid.cos.ap-guangzhou.myqcloud.com`），
  桶下按程序各放一个子目录，目录名即该程序的 Prefix（如 `AutoPddTax`）。新程序接入只加一个新前缀。
- **客户端读取（公有读）**：直接使用，无需任何密钥，完整性由 HTTPS 与强制 SHA256 兜底。
- **客户端读取（私有读）**：提供 SecretID 与 SecretKey，库为每次请求生成 15 分钟有效的签名 URL。
- **发布上传（私有写）**：需要 SecretID 与 SecretKey，仅发布端（build.sh）使用，**客户端一律不需要**。

## 发布新版本

用内置的通用发布命令，一次完成「上传产物 + 自动算校验和 + 生成上传 version.json」：

```bash
cosup-publish \
  -bucket https://bucket-appid.cos.ap-guangzhou.myqcloud.com \
  -prefix AutoPddTax -version 2.0.0 \
  -secret-id $COS_SECRET_ID -secret-key $COS_SECRET_KEY \
  -asset "windows/amd64=dist/拼多多商店自动开票系统.exe" \
  -note "发行说明"
```

- `-asset` 可多次（每个平台一条）；`-bucket` 与密钥缺省时回退到环境变量
  `COS_BUCKET` / `COS_SECRET_ID` / `COS_SECRET_KEY`，避免密码进命令行历史。
- 产物上传到 `{Prefix}/{Version}/{GOOS}/{GOARCH}/{文件名}`（版本 + 平台双层子目录，
  防多平台同名覆盖）；加 `-flat` 则改为 `{Prefix}/{GOOS}/{GOARCH}/{文件名}`（去版本段，
  桶只留最新，不保留历史）。version.json 固定上传到 `{Prefix}/version.json`。
- 顺序自动处理：**先全部产物、后 version.json**，旧客户端不会拿到指向 404 的地址。

### 接入 build.sh / deploy.sh（建议把构建与发布拆成两个脚本）

推荐**构建与发布分开**，职责单一、可只构建不上传、可单独重发某版本。
版本号**不依赖 git tag**：以客户端 `internal/update` 里的 `var Version` 为基准，build 时把
最后一段（PATCH）数字 +1、注入构建并回写，从而每次构建自动递增。

```sh
# build.sh —— 只构建，不发布；修改前先确保 internal/update/update.go 里 Version 为三段数字（如 1.0.0）
#!/usr/bin/env bash
set -e
UP="$PWD/internal/update/update.go"
CUR=$(grep -oE '^var Version = "[^"]*"' "$UP" | sed -E 's/.*"([^"]*)"/\1/')
echo "$CUR" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$' || { echo "Version 非三段数字" >&2; exit 1; }
MAJOR=$(echo "$CUR"|cut -d. -f1); MINOR=$(echo "$CUR"|cut -d. -f2); PATCH=$(echo "$CUR"|cut -d. -f3)
NEXT="$MAJOR.$MINOR.$((PATCH+1))"
go build -trimpath -ldflags "-s -w -H windowsgui -X autopddtax/internal/update.Version=$NEXT" \
  -o "dist/拼多多商店自动开票系统.exe" .
sed -i "s|^var Version = \"[^\"]*\"|var Version = \"$NEXT\"|" "$UP"   # 回写，下次构建继续 +1

# deploy.sh —— 专用发布：读 dist/ 产物上传，桶地址与密钥写死，版本读 update.go
#!/usr/bin/env bash
BUCKET="https://bucket-appid.cos.ap-guangzhou.myqcloud.com"   # 与 update 模块的 bucketRoot 一致
PREFIX="AutoPddTax"
SECRET_ID="AKID..."    # 私有写所需；客户端读不涉及
SECRET_KEY="..."       # 写死进脚本即可（仓库私有）
V=$(grep -oE '^var Version = "[^"]*"' internal/update/update.go | sed -E 's/.*"([^"]*)"/\1/')
go run github.com/leafsnow/cos-updater/cmd/publish \
  -bucket "$BUCKET" -prefix "$PREFIX" -version "$V" \
  -secret-id "$SECRET_ID" -secret-key "$SECRET_KEY" \
  -asset "windows/amd64=dist/拼多多商店自动开票系统.exe" -note "v$V 自动发布"
```

- `-ldflags -X` 把 `$NEXT` 注入客户端，deploy 的 `-version "$V"` 读同一份 update.go（此时已被 build 回写为
  `$NEXT`），二者一致，客户端展示的版本与 version.json 里的版本永远吻合。
- **桶地址**与**密钥**都直接写死进 deploy.sh，并与客户端 `internal/update` 的 `bucketRoot` 常量一致，
  避免双份维护；`-bucket` 也可省略改交环境变量 `COS_BUCKET`。
- **版本号权威来源 = update.go 的 `var Version`**：build 读它、PATCH+1 后回写，源码里的数字即
  「上一个已发布版本」。deploy 读同一份源码作为发布版本，`-v` 仍可手动覆盖（重发历史版本用）。
- 密钥写死进脚本后，deploy 不再读环境变量；若改回环境变量，缺省时 deploy.sh 应安全跳过。

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

## 测试

```bash
go test ./... -count=1
```

48 个自动化测试全部通过，覆盖：版本比较、校验和解析、URL 拼接与转义、
COS 签名 URL 形态、平台选择、完整更新流程（公有读/私有读）、
校验失败回滚、ctx 取消下载、进度回调粒度与 100% 补发。

Windows 手动验证清单（自动化测试无法覆盖的环节）：

1. 替换正在运行的 exe：自更新 + Restart() 后确认无崩溃且新版本生效
2. 私有读桶实测：签名 URL 对真实 COS 服务的兼容性无法用 httptest 覆盖，已用真实桶验证
   GET / PUT / DELETE 及含中文对象键的签名均可正常使用。若遇 403，多为密钥、时钟偏差，
   或对象键含中文——签名 HttpString 的 UriPathname 须用「URL 解码后的原始字节」（原文，
   含中文/空格），而非 URL 编码形式（见 cosauth.go）。
3. 大文件进度回调：观察 OnProgress 触发频率与最终 100% 补发
4. Unix 权限：Linux/macOS 上更新后确认可执行权限保持 0755

## 许可证

TODO
