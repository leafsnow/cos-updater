package cosupdater

import "errors"

// 库的统一错误前缀。
const errPrefix = "cos-updater: "

// 预定义错误。调用方可用 errors.Is 判断具体失败原因，
// 其余错误（网络、IO 等）一律原样或包装后返回，不做内部重试。
var (
	// ErrInvalidConfig 配置缺失或格式非法（如 BucketURL 没有 scheme、必填项为空）。
	ErrInvalidConfig = errors.New(errPrefix + "配置无效")

	// ErrSecretPair SecretID 与 SecretKey 必须同时提供（只给了一个）。
	ErrSecretPair = errors.New(errPrefix + "SecretID 与 SecretKey 必须同时提供")

	// ErrInvalidVersionInfo 版本文件 JSON 缺少必需字段或结构不符合约定
	//（缺少 version / platforms，platforms 为空等）。
	ErrInvalidVersionInfo = errors.New(errPrefix + "版本文件格式无效")

	// ErrPlatformNotSupported 版本文件的 platforms 中没有当前平台（GOOS/GOARCH）的条目。
	ErrPlatformNotSupported = errors.New(errPrefix + "版本文件中没有当前平台的下载信息")

	// ErrChecksumRequired 版本文件缺少 checksum 或格式不是 sha256:<64位十六进制>。
	// 本库强制校验和，缺省即拒绝下载。
	ErrChecksumRequired = errors.New(errPrefix + "缺少 checksum（格式须为 sha256:<64位十六进制>）")

	// ErrChecksumMismatch 下载文件的 SHA256 与版本文件声明的不一致。
	// 此时临时文件已删除，原程序未被改动。
	ErrChecksumMismatch = errors.New(errPrefix + "下载文件 SHA256 校验失败")

	// ErrInvalidDownloadURL 版本文件中的 download_url 非法
	//（例如私有读模式下却给了绝对 URL，库无法对其签名）。
	ErrInvalidDownloadURL = errors.New(errPrefix + "download_url 无效")

	// ErrCDNFailed CDN 缓存刷新/预热存在失败项（某批提交失败或任务终态非 success）。
	// 不阻断已成功的发布，调用方用它识别「CDN 环节未完全生效」。
	ErrCDNFailed = errors.New(errPrefix + "CDN 刷新/预热存在失败项")
)
