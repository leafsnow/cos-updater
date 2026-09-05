package cosupdater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/inconshreveable/go-update"
)

// ApplyUpdate 执行一次完整更新：下载 -> SHA256 校验 -> 原子替换。
//
// info 通常来自 CheckUpdate，也可手动构造。
// 下载过程中按 64KB 粒度触发 cfg.OnProgress；ctx 取消时中断下载并返回其错误。
//
// 失败保证：校验不通过或下载中断时，原文件不会被改动，临时文件已被清理；
// 替换由 go-update 原子完成，失败时同样保留原文件。
// Unix 系统下替换成功后会补一次 chmod 0755 确保可执行权限。
func ApplyUpdate(ctx context.Context, cfg *Config, info *VersionInfo) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	asset, err := info.assetForCurrentPlatform()
	if err != nil {
		return err
	}
	wantSum, err := parseChecksum(asset.Checksum)
	if err != nil {
		return err
	}

	targetPath := cfg.TargetPath
	if targetPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("获取当前程序路径失败: %w", err)
		}
		targetPath = exe
	}
	// go-update 替换前需要先打开原文件，提前检查以给出可读的错误。
	if _, err := os.Stat(targetPath); err != nil {
		return fmt.Errorf("目标文件 %s 不存在或不可访问（自更新要求目标文件已存在）: %w", targetPath, err)
	}

	fileURL, err := resolveFileURL(cfg, asset.DownloadURL)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "cos-updater-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	replaced := false
	defer func() {
		if !replaced {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if err := downloadToFile(ctx, fileURL, tmp, cfg.OnProgress); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}

	if err := verifyChecksum(tmpName, wantSum); err != nil {
		return err
	}

	patch, err := os.Open(tmpName)
	if err != nil {
		return fmt.Errorf("打开临时文件失败: %w", err)
	}
	// go-update 不负责关闭 patch。
	if err := update.Apply(patch, update.Options{TargetPath: targetPath}); err != nil {
		patch.Close()
		return fmt.Errorf("替换目标文件失败: %w", err)
	}
	patch.Close()

	os.Remove(tmpName) // 临时文件已完成使命，清理失败不影响更新结果
	replaced = true

	if runtime.GOOS != "windows" {
		_ = os.Chmod(targetPath, 0755)
	}
	return nil
}

// verifyChecksum 计算文件 SHA256 并与期望摘要比较。
func verifyChecksum(path string, want []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开临时文件失败: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("计算校验和失败: %w", err)
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return ErrChecksumMismatch
	}
	return nil
}

// resolveFileURL 把版本文件或产物地址解析成可直接请求的 URL：
//   - 绝对 URL：仅公有读模式允许（私有读模式下库无法对任意外部地址签名）；
//   - 相对路径：私有读模式按 COS 签名生成带 q-sign-* 的 URL，
//     公有读模式直接拼接 BucketURL（路径段做 RFC 3986 转义，支持中文文件名）。
func resolveFileURL(cfg *Config, raw string) (string, error) {
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		if hasSecret(cfg) {
			return "", fmt.Errorf("%w: 私有读模式下 download_url 必须为桶内相对路径，收到绝对 URL %q", ErrInvalidDownloadURL, raw)
		}
		return raw, nil
	}
	if hasSecret(cfg) {
		return cosObjectURL(cfg.BucketURL, raw, cfg.SecretID, cfg.SecretKey, time.Now())
	}
	return strings.TrimRight(cfg.BucketURL, "/") + "/" + escapeCosKey(strings.TrimLeft(raw, "/")), nil
}
