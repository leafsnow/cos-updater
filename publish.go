package cosupdater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PublishAsset 描述发布时要上传的一个平台产物。
type PublishAsset struct {
	// GOOS 目标操作系统，如 "windows"、"linux"。
	GOOS string

	// GOARCH 目标架构，如 "amd64"、"arm64"。
	GOARCH string

	// FilePath 本地产物文件路径，如 "dist/拼多多商店自动开票系统.exe"。
	FilePath string
}

// PublishConfig 是发布（上传）的配置。
//
// 与客户端 Config 的语义不同：发布针对的是"私有写"桶，
// 必须同时提供 SecretID 与 SecretKey。桶根地址全局唯一，
// 每个程序通过 Prefix（桶下子目录名，如 "AutoPddTax"）区分。
type PublishConfig struct {
	// BucketURL COS 桶根访问地址，须含 scheme，如
	// "https://gxhyt-1303168605.cos.ap-guangzhou.myqcloud.com"。
	BucketURL string

	// Prefix 程序在桶根下的子目录名（即用户说的"一个桶放多个程序，不同程序分目录"）。
	// 产物上传到 {Prefix}/{Version}/{文件名}，版本文件上传到 {Prefix}/version.json。
	Prefix string

	// Version 本次发布的版本号，写入 version.json 的 version 字段。
	Version string

	// ReleaseNotes 发行说明，可选。
	ReleaseNotes string

	// SecretID 腾讯云 SecretID（私有写必需）。
	SecretID string

	// SecretKey 腾讯云 SecretKey（私有写必需）。
	SecretKey string
}

// validate 校验发布配置。
func (c *PublishConfig) validate() error {
	u, err := url.Parse(c.BucketURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%w: BucketURL 须为完整的 http(s) 地址，"+
			"如 https://bucket-appid.cos.ap-guangzhou.myqcloud.com", ErrInvalidConfig)
	}
	if strings.TrimSpace(c.Prefix) == "" {
		return fmt.Errorf("%w: Prefix 不能为空（程序在桶下的子目录名）", ErrInvalidConfig)
	}
	if strings.TrimSpace(c.Version) == "" {
		return fmt.Errorf("%w: Version 不能为空", ErrInvalidConfig)
	}
	if (c.SecretID == "") != (c.SecretKey == "") {
		return fmt.Errorf("%w: SecretID 与 SecretKey 必须同时提供", ErrSecretPair)
	}
	return nil
}

// Publish 执行一次发布：按顺序上传产物与版本文件。
//
// 顺序保证：先上传全部产物、最后上传 version.json——JSON 里写的 checksum
// 指向产物，先传 JSON 会让旧版本客户端拿到指向 404 的下载地址（与 README 一致）。
//
//  1. 对每个产物计算 SHA256 并上传到 {Prefix}/{Version}/{文件名}；
//  2. 生成 version.json（version / release_notes / platforms，含 checksum）；
//  3. 上传 version.json 到 {Prefix}/version.json。
//
// 产物路径以版本子目录存放，避免同名产物在多版本间覆盖。ctx 可取消上传。
func Publish(ctx context.Context, cfg PublishConfig, assets []PublishAsset) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	if cfg.SecretID == "" || cfg.SecretKey == "" {
		return fmt.Errorf("%w: 发布上传（私有写）需要同时提供 SecretID 与 SecretKey", ErrSecretPair)
	}
	if len(assets) == 0 {
		return fmt.Errorf("%w: 至少提供一个产物", ErrInvalidConfig)
	}

	platforms := make(map[string]*PlatformAsset, len(assets))
	for _, a := range assets {
		if a.GOOS == "" || a.GOARCH == "" {
			return fmt.Errorf("%w: 产物 %s 缺少 GOOS/GOARCH", ErrInvalidConfig, a.FilePath)
		}
		info, err := os.Stat(a.FilePath)
		if err != nil {
			return fmt.Errorf("产物 %s 不可读: %w", a.FilePath, err)
		}
		base := filepath.Base(a.FilePath)
		objectKey := joinObjKey(cfg.Prefix, cfg.Version, base)

		sum, err := fileSHA256(a.FilePath)
		if err != nil {
			return err
		}
		if err := uploadFileToBucket(ctx, cfg, objectKey, a.FilePath, info.Size()); err != nil {
			return err
		}
		platforms[a.GOOS+"/"+a.GOARCH] = &PlatformAsset{
			DownloadURL: objectKey,
			Checksum:    "sha256:" + sum,
		}
	}

	manifest := VersionInfo{
		Version:      cfg.Version,
		ReleaseNotes: cfg.ReleaseNotes,
		Platforms:    platforms,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("序列化版本文件失败: %w", err)
	}
	versionKey := joinObjKey(cfg.Prefix, "version.json")
	return uploadBytesToBucket(ctx, cfg, versionKey, data)
}

// joinObjKey 拼接对象键，忽略空段与首尾斜杠（产物按版本子目录、版本文件固定位置）。
func joinObjKey(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.Trim(p, "/"); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "/")
}

// fileSHA256 计算文件 SHA256 并返回小写十六进制串。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开产物 %s 失败: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("计算产物 %s 校验和失败: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// uploadFileToBucket 把本地文件上传为桶内对象（PUT，私有写签名）。
func uploadFileToBucket(ctx context.Context, cfg PublishConfig, objectKey, path string, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开产物 %s 失败: %w", path, err)
	}
	defer f.Close()
	return uploadToBucket(ctx, cfg, objectKey, f, size)
}

// uploadBytesToBucket 把内存中的 bytes 上传为桶内对象（PUT，私有写签名）。
func uploadBytesToBucket(ctx context.Context, cfg PublishConfig, objectKey string, data []byte) error {
	return uploadToBucket(ctx, cfg, objectKey, bytes.NewReader(data), int64(len(data)))
}

// uploadToBucket 用 COS 预签名 URL 执行 PUT 上传。
func uploadToBucket(ctx context.Context, cfg PublishConfig, objectKey string, body io.Reader, size int64) error {
	signed, err := cosObjectURLMethod(http.MethodPut, cfg.BucketURL, objectKey, cfg.SecretID, cfg.SecretKey, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, signed, body)
	if err != nil {
		return fmt.Errorf("构造上传请求失败: %w", err)
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := downloadClient.Do(req) // 复用无总超时客户端的响应头限制，取消交给 ctx
	if err != nil {
		return fmt.Errorf("上传 %s 失败: %w", objectKey, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msgb, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("上传 %s 失败：HTTP %d %s", objectKey, resp.StatusCode, strings.TrimSpace(string(msgb)))
	}
	return nil
}
