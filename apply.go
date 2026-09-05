package cosupdater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// NextExecutablePath 计算本次更新将要安装到的本地新程序路径。
//
// 规则：与当前运行程序（cfg.TargetPath，为空时取 os.Executable()）同目录，
// 文件名为「原名去掉扩展名 + _v + 版本号 + 原扩展名」，如
// "合洋泰销售提成分析工具_v1.0.3.exe"。版本号中的前导 'v' 会被去除，
// 避免出现 "_vv1.0.3" 这类双 v 文件名。返回绝对路径。
func NextExecutablePath(cfg *Config, info *VersionInfo) (string, error) {
	targetPath := cfg.TargetPath
	if targetPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("获取当前程序路径失败: %w", err)
		}
		targetPath = exe
	}
	dir := filepath.Dir(targetPath)
	base := filepath.Base(targetPath)
	ext := filepath.Ext(base)
	nameNoExt := strings.TrimSuffix(base, ext)
	ver := strings.TrimPrefix(strings.TrimSpace(info.Version), "v")
	return filepath.Join(dir, fmt.Sprintf("%s_v%s%s", nameNoExt, ver, ext)), nil
}

// ApplyUpdate 执行一次完整更新：下载 -> SHA256 校验 -> 安装到同目录独立版本文件。
//
// 本函数不再原地替换正在运行的程序（旧 go-update 方案在 Windows 上因「正在运行的
// exe 只能重命名不能覆盖」而被迫把老程序改名成 .old 并加隐藏属性，且替换运行中的
// 进程后重启会拒绝访问）。新方案把新版下载到带版本号的独立文件（见
// NextExecutablePath），运行中的原程序保持不变、不被改名、不被隐藏；调用方拿到返回
// 的新路径后用 Restart 启动它即可生效。
//
// 失败保证：校验不通过或下载中断时，原程序不会被改动，临时文件已被清理；安装失败时
// 同样保留原程序。成功时返回新程序绝对路径。
func ApplyUpdate(ctx context.Context, cfg *Config, info *VersionInfo) (string, error) {
	if err := cfg.validate(); err != nil {
		return "", err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	asset, err := info.assetForCurrentPlatform()
	if err != nil {
		return "", err
	}
	wantSum, err := parseChecksum(asset.Checksum)
	if err != nil {
		return "", err
	}

	newPath, err := NextExecutablePath(cfg, info)
	if err != nil {
		return "", err
	}

	fileURL, err := resolveFileURL(cfg, asset.DownloadURL)
	if err != nil {
		return "", err
	}

	// 临时文件建在与新程序同目录，保证最终 rename 不发生跨盘移动。
	tmp, err := os.CreateTemp(filepath.Dir(newPath), ".cosup-*.tmp")
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败: %w", err)
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
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("写入临时文件失败: %w", err)
	}

	if err := verifyChecksum(tmpName, wantSum); err != nil {
		return "", err
	}

	// 安装到新路径。同版本文件可能已存在（重装），Windows 上 rename 到已存在目标会
	// 失败，故先尝试删除旧目标再落位。运行中的原程序（不带版本号）不受影响。
	if err := os.Rename(tmpName, newPath); err != nil {
		if rmErr := os.Remove(newPath); rmErr == nil {
			if err2 := os.Rename(tmpName, newPath); err2 != nil {
				return "", fmt.Errorf("安装新版本失败: %w", err2)
			}
		} else {
			return "", fmt.Errorf("安装新版本失败: %w", err)
		}
	}
	replaced = true

	if runtime.GOOS != "windows" {
		_ = os.Chmod(newPath, 0755)
	}
	return newPath, nil
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
