// Package cosupdater 提供基于腾讯云 COS 的极简程序自更新能力：
// 检查新版本 -> 下载 -> SHA256 校验 -> 安装到独立版本文件（不动、不隐藏运行中的原程序），
// 支持下载进度回调与 ctx 取消，公有读/私有读桶均可用（私有读为手写签名，零 SDK 依赖）。
//
// 最小用法：
//
//	cfg := &cosupdater.Config{
//	    BucketURL:       "https://bucket-appid.cos.ap-guangzhou.myqcloud.com",
//	    VersionFilePath: "updates/version.json",
//	    CurrentVersion:  "1.0.0",
//	}
//	has, info, err := cosupdater.CheckUpdate(ctx, cfg)
//	if err == nil && has {
//	    err = cosupdater.ApplyUpdate(ctx, cfg, info)
//	}
//
// 库内部不维护任何状态，所有函数可安全并发调用；
// 但对同一 TargetPath 的更新须由调用方保证串行。
package cosupdater

import (
	"context"
	"encoding/json"
	"fmt"
)

// CheckUpdate 检查是否有新版本。
//
// 返回是否有更新、远程版本信息与错误：
//   - 网络/解析/格式问题时返回 error；
//   - 远程版本与当前相同或更低时返回 has=false（info 仍携带解析结果，可供展示）；
//   - 远程版本更新时返回 has=true。
//
// 注意：has=true 不代表当前平台可更新——platforms 中可能没有当前平台条目，
// 需要预检的调用方可通过 info.AssetForPlatform(runtime.GOOS, runtime.GOARCH) 判断。
func CheckUpdate(ctx context.Context, cfg *Config) (bool, *VersionInfo, error) {
	if err := cfg.validate(); err != nil {
		return false, nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	fileURL, err := resolveFileURL(cfg, cfg.VersionFilePath)
	if err != nil {
		return false, nil, err
	}
	data, err := fetchFile(ctx, fileURL)
	if err != nil {
		return false, nil, err
	}

	var info VersionInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return false, nil, fmt.Errorf("%w: JSON 解析失败: %v", ErrInvalidVersionInfo, err)
	}
	if info.Version == "" {
		return false, nil, fmt.Errorf("%w: 缺少 version 字段", ErrInvalidVersionInfo)
	}
	if len(info.Platforms) == 0 {
		return false, nil, fmt.Errorf("%w: 缺少 platforms 字段", ErrInvalidVersionInfo)
	}

	newer, err := isNewerVersion(cfg.CurrentVersion, info.Version)
	if err != nil {
		return false, nil, err
	}
	return newer, &info, nil
}

// RunUpdate 是 CheckUpdate + ApplyUpdate 的组合，一步完成静默更新：
// 有新版本就下载并替换，没有则返回 nil。不包含任何用户确认逻辑。
func RunUpdate(ctx context.Context, cfg *Config) error {
	has, info, err := CheckUpdate(ctx, cfg)
	if err != nil {
		return err
	}
	if !has {
		return nil
	}
	_, err = ApplyUpdate(ctx, cfg, info)
	return err
}
