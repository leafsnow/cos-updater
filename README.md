# cos-updater

基于腾讯云 COS 的极简 Go 程序自更新库：检查新版本 → 下载 → SHA256 校验 → 退役旧程序（改名 `.old`）→ 落位**固定名**新程序 → 自动启动新版本。


## 安装

```bash
go get github.com/leafsnow/cos-updater
```

## 使用

### version.json 规范

1.放在桶内 `{Prefix}/version.json`（Prefix 即该程序在桶根下的子目录名）。
2.`platforms` 必需，key 为 `GOOS/GOARCH`。
3.每个平台条目须含 `download_url`（**相对桶根的路径**，含 Prefix；或绝对 URL——仅公有读模式允许）与 `checksum`（强制，格式 `sha256:<64位十六进制>`）。`download_url` 指向的文件名即程序固定名（不带版本号）。
4.`release_notes` 可选，字符串数组，每条是一项更新日志；客户端读取兼容旧版单个字符串。

```json
{
  "version": "2.0.0",
  "release_notes": ["修复了 6 月账单导出的合计行错误", "优化导出速度"],
  "platforms": {
    "windows/amd64": {
      "download_url": "AutoPddTax/2.0.0/windows/amd64/AutoPddTax.exe",
      "checksum": "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
    }
  }
}
```

### API

| 函数 | 说明 |
|---|---|
| `CheckUpdate(ctx, cfg)` | 检查是否有新版本；版本相同或更低返回 `has=false`，`info` 仍携带解析结果可供展示 |
| `ApplyUpdate(ctx, cfg, info)` | 下载 → 强制 SHA256 校验 → 退役旧程序（改名为 `.old-<时间戳>`）→ 新版本落位到固定名 → 返回固定名路径；失败时原程序保留、临时文件清理 |
| `RunUpdate(ctx, cfg)` | CheckUpdate + ApplyUpdate 的静默组合（丢弃路径）；无新版本时返回 `nil` |
| `NextExecutablePath(cfg, info)` | 计算新版将安装到的**固定名**路径（剥掉基准名里的 `_v<版本>` 后缀，程序名永不变），供确认弹窗展示 |
| `Retire(path)` | 把正在运行的旧程序改名为同目录 `<原名>.old-<时间戳>`：名字唯一（冲突自动递增后缀）、保留多份、可回滚。已由 ApplyUpdate 落位前自动调用，可单独复用 |
| `Restart(path ...string)` | 重启让更新生效；传新版本路径，为空时用 `os.Executable()`。Unix 替换进程映像且不返回，Windows 安排延迟启动后返回 |
| `AssetForPlatform(info, goos, arch)` | 预检 platforms 中是否有指定平台的产物，便于更新前预判 |

常见错误可用 `errors.Is` 判断，如 `ErrChecksumMismatch`、`ErrInvalidConfig`、`ErrPlatformNotSupported` 等。

## 发布新版本

用内置的 `cosup-publish` 命令，一次完成「上传产物 + 自动算校验和 + 生成上传 version.json」：

```bash
cosup-publish \
  -bucket https://xxxxx.com \
  -prefix AutoPddTax -version 2.0.0 \
  -secret-id $COS_SECRET_ID -secret-key $COS_SECRET_KEY \
  -asset "windows/amd64=dist/AutoPddTax.exe" \
  -note "修复了 6 月账单导出的合计行错误" -note "优化导出速度"
```

| 参数 | 说明 |
|---|---|
| `-bucket` / `-prefix` / `-version` | 桶地址、该程序在桶下的子目录名、版本号 |
| `-secret-id` / `-secret-key` | 发布密钥；`-bucket` 与二者缺省时分别回退环境变量 `COS_BUCKET` / `COS_SECRET_ID` / `COS_SECRET_KEY`，避免密码进命令行历史 |
| `-asset` | 可多次，每个平台一条，格式 `GOOS/GOARCH=本地文件路径` |
| `-note` | 可多次，每条对应 release_notes 数组的一项 |
| `-flat` | 产物路径不带版本子目录（桶只留最新，不保留历史） |

- 上传到 `{Prefix}/{Version}/{GOOS}/{GOARCH}/{文件名}`（版本 + 平台双层子目录，防多平台同名覆盖）；加 `-flat` 改为 `{Prefix}/{GOOS}/{GOARCH}/{文件名}`。version.json 固定上传到 `{Prefix}/version.json`。
- 顺序自动处理：**先全部产物、后 version.json**，旧客户端不会拿到指向 404 的地址。
