package cosupdater

import (
	"fmt"
	"net/url"
)

// Config 是库的全部配置。只有前三个字段必填，其余可选。
type Config struct {
	// -------- 必填 --------

	// BucketURL COS 桶访问地址，须含 scheme，如
	// "https://my-bucket-1250000000.cos.ap-guangzhou.myqcloud.com"。
	BucketURL string

	// VersionFilePath 版本文件在桶内的对象路径，如 "updates/version.json"。
	// 私有读模式下它同样是签名对象，同样需要下载权限。
	VersionFilePath string

	// CurrentVersion 当前程序版本号，如 "1.0.0" 或 "v1.0.0"（两种写法均可解析）。
	// 通常由 -ldflags "-X main.version=xxx" 注入后传入。
	CurrentVersion string

	// -------- 可选（私有读桶需提供） --------

	// SecretID 腾讯云 SecretID。与 SecretKey 必须同时提供或同时为空。
	// 均为空时按公有读处理，直接走 HTTP GET。
	SecretID string

	// SecretKey 腾讯云 SecretKey。
	SecretKey string

	// -------- 可选（高级定制） --------

	// TargetPath 要替换的目标文件路径。为空时默认 os.Executable()。
	// 自更新要求目标文件已存在（库需要先读取原文件再原子替换）。
	TargetPath string

	// OnProgress 下载进度回调，单位字节。total 为响应 Content-Length；
	// 服务器未返回长度时 total 为 0（此时仍会持续回调，仅 downloaded 增长）。
	//
	// 注意：回调在下载 goroutine 中同步触发，GUI 程序须自行切换到 UI 线程
	//（例如 Fyne 中的 fyne.Do(...)）。每下载约 64KB 触发一次，
	// 下载结束时会补发一次保证最终能到达 100%。
	OnProgress func(downloaded, total int64)
}

// hasSecret 报告是否处于私有读模式（提供了密钥）。
func hasSecret(c *Config) bool {
	return c.SecretID != ""
}

// validate 校验配置的必填项与格式，失败时返回包装了 sentinel 错误的结果。
func (c *Config) validate() error {
	if c == nil {
		return fmt.Errorf("%w: cfg 不能为 nil", ErrInvalidConfig)
	}
	u, err := url.Parse(c.BucketURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%w: BucketURL 须为完整的 http(s) 地址，"+
			"如 https://bucket-appid.cos.ap-guangzhou.myqcloud.com", ErrInvalidConfig)
	}
	if c.VersionFilePath == "" {
		return fmt.Errorf("%w: VersionFilePath 不能为空", ErrInvalidConfig)
	}
	if c.CurrentVersion == "" {
		return fmt.Errorf("%w: CurrentVersion 不能为空", ErrInvalidConfig)
	}
	if (c.SecretID == "") != (c.SecretKey == "") {
		return fmt.Errorf("%w（当前只提供了其中一个）", ErrSecretPair)
	}
	return nil
}
