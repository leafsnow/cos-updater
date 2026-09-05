package cosupdater

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"runtime"
	"strings"

	"github.com/hashicorp/go-version"
)

// PlatformAsset 描述某一个平台（GOOS/GOARCH）的更新产物。
type PlatformAsset struct {
	// DownloadURL 产物在桶内的对象路径（推荐，如 "syncledger_v2.0.0.exe"），
	// 或完整的 http(s) 绝对 URL（仅公有读模式允许，私有读模式会返回错误）。
	DownloadURL string `json:"download_url"`

	// Checksum 下载产物的 SHA256，格式 "sha256:<64位十六进制>"。
	// 本库强制校验：缺失或格式不对时拒绝下载。
	Checksum string `json:"checksum"`
}

// VersionInfo 是版本文件 version.json 的结构。
//
// 约定的 JSON 格式：
//
//	{
//	  "version": "2.0.0",
//	  "release_notes": "修复了 6 月账单导出的合计行错误",
//	  "platforms": {
//	    "windows/amd64": { "download_url": "syncledger_v2.0.0.exe", "checksum": "sha256:..." },
//	    "linux/amd64":   { "download_url": "syncledger_v2.0.0_linux", "checksum": "sha256:..." }
//	  }
//	}
type VersionInfo struct {
	// Version 远程最新版本号，与 Config.CurrentVersion 用相同规则比较。
	Version string `json:"version"`

	// ReleaseNotes 更新说明，可选。供确认弹窗展示。
	ReleaseNotes string `json:"release_notes,omitempty"`

	// Platforms 各平台的下载产物，key 为 "GOOS/GOARCH"（如 "windows/amd64"）。必需。
	Platforms map[string]*PlatformAsset `json:"platforms"`
}

// AssetForPlatform 返回指定平台的下载产物。
// 没有对应条目时返回包装了 ErrPlatformNotSupported 的错误。
// 公开此方法便于调用方在展示"更新"按钮前预检平台支持情况。
func (v *VersionInfo) AssetForPlatform(goos, arch string) (*PlatformAsset, error) {
	if v == nil {
		return nil, fmt.Errorf("%w: 版本信息为 nil", ErrInvalidVersionInfo)
	}
	asset, ok := v.Platforms[goos+"/"+arch]
	if !ok || asset == nil || asset.DownloadURL == "" {
		return nil, fmt.Errorf("%w: %s/%s", ErrPlatformNotSupported, goos, arch)
	}
	return asset, nil
}

// assetForCurrentPlatform 返回当前运行平台的下载产物。
func (v *VersionInfo) assetForCurrentPlatform() (*PlatformAsset, error) {
	return v.AssetForPlatform(runtime.GOOS, runtime.GOARCH)
}

// checksumRe 校验和的合法格式：sha256 前缀 + 64 位十六进制。
var checksumRe = regexp.MustCompile(`^sha256:([0-9a-fA-F]{64})$`)

// parseChecksum 解析 checksum 字段并返回二进制摘要。
// 强制校验：缺失或格式不对均返回 ErrChecksumRequired。
func parseChecksum(s string) ([]byte, error) {
	m := checksumRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return nil, ErrChecksumRequired
	}
	sum, err := hex.DecodeString(m[1])
	if err != nil {
		return nil, fmt.Errorf("%w: checksum 十六进制解析失败: %v", ErrChecksumRequired, err)
	}
	return sum, nil
}

// isNewerVersion 判断远程版本是否比当前版本新。
// 兼容 "1.0.0" 与 "v1.0.0" 两种写法，支持语义化版本与预发布号
// （如 1.0.0-rc.1 < 1.0.0）。任一版本号无法解析时返回错误。
func isNewerVersion(current, remote string) (bool, error) {
	cv, err := version.NewVersion(current)
	if err != nil {
		return false, fmt.Errorf("当前版本号 %q 无法解析: %w", current, err)
	}
	rv, err := version.NewVersion(remote)
	if err != nil {
		return false, fmt.Errorf("远程版本号 %q 无法解析: %w", remote, err)
	}
	return rv.GreaterThan(cv), nil
}
