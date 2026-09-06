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

	"github.com/hashicorp/go-version"
)

// NextExecutablePath 计算本次更新将要安装到的本地程序路径。
//
// 规则：与当前运行程序（cfg.TargetPath，为空时取 os.Executable()）同目录，
// 文件名为「稳定固定名」——即「原名剥掉 _v<版本> 后缀 + 原扩展名」，如
// "合洋泰销售提成分析工具.exe"。基准名本身可能带版本号（如旧版残留的
// "合洋泰销售提成分析工具_v1.0.6.exe"），会被剥掉，保证程序名永远不变、
// 连续更新不会产生 "…_v1.0.6_v1.0.7.exe" 这类嵌套名。返回绝对路径。
// info 参数保留仅用于签名兼容：固定名不再包含版本号。
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
	stem := strings.TrimSuffix(base, ext)
	// 反复剥掉尾部 "_v<语义版本>" 后缀，直到无法再剥，得到稳定基础名。
	// 否则连续的版本后缀（如 "…_v1.0.6_v1.0.7"）只会去掉最末一段，剩下
	// "…_v1.0.6" 仍带版本号，连续更新会继续嵌套。循环确保剥到基础名。
	for {
		if i := strings.LastIndex(stem, "_v"); i >= 0 {
			if _, err := version.NewVersion(stem[i+2:]); err == nil {
				stem = stem[:i]
				continue
			}
		}
		break
	}
	return filepath.Join(dir, stem+ext), nil
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

	// 安装到固定名。目标固定名可能正在运行（Windows 上运行中的 exe 不能被覆盖/删除，
	// 只能改名），故必须先把它退役成 ".old-<时间戳>" 腾出名字，再落位新版本。这一步
	// 内部调用 Retire，会保留多份历史备份、不隐藏。失败表示退役或落位不成功，原程序仍在。
	if _, err := os.Stat(newPath); err == nil {
		if _, rerr := Retire(newPath); rerr != nil {
			return "", rerr
		}
	}
	if err := os.Rename(tmpName, newPath); err != nil {
		return "", fmt.Errorf("安装新版本失败: %w", err)
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
