// Command publish 是一个通用的 COS 发布工具：上传产物的新版本到给定桶，
// 并生成/上传对应的 version.json。供各程序 build.sh 在构建完成后自动调用。
//
// 用法：
//
//	cosup-publish -prefix AutoPddTax -version 1.0.0 \
//	  -secret-id $COS_SECRET_ID -secret-key $COS_SECRET_KEY \
//	  -bucket https://gxhyt-1303168605.cos.ap-guangzhou.myqcloud.com \
//	  -asset "windows/amd64=dist/拼多多商店自动开票系统.exe" [-note 说明] [-note 说明] [-flat]
//
// -bucket / 密钥缺省时依次回退到环境变量 COS_BUCKET / COS_SECRET_ID / COS_SECRET_KEY。
// 同一个桶下放多个程序时，每个程序共用桶根地址、仅以不同的 -prefix 区分。
//
// CDN 刷新/预热（可选）：传入 -cdn-domain（或环境变量 COS_CDN_DOMAIN）后，发布上传成功会
// 自动刷新 CDN（version.json 恒定刷新），并按布局处理产物——默认布局预热产物；
// -flat 布局先刷新产物（清旧缓存）再预热。CDN 失败不阻断发布，仅打印警告。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	cosupdater "github.com/leafsnow/cos-updater"
)

var (
	assets []cosupdater.PublishAsset
	notes  []string
)

func main() {
	bucket := flag.String("bucket", "", "COS 桶根地址（缺省取环境变量 COS_BUCKET）")
	prefix := flag.String("prefix", "", "程序在桶下的子目录名（如 AutoPddTax），必填")
	version := flag.String("version", "", "本次发布的版本号，必填")
	flat := flag.Bool("flat", false, "产物路径不带版本子目录（仅 {prefix}/{GOOS}/{GOARCH}/{文件名}，桶只留最新）")
	secretID := flag.String("secret-id", "", "腾讯云 SecretID（缺省取 COS_SECRET_ID）")
	secretKey := flag.String("secret-key", "", "腾讯云 SecretKey（缺省取 COS_SECRET_KEY）")
	cdnDomain := flag.String("cdn-domain", "", "CDN 访问域名，非空则发布后刷新/预热 CDN（缺省取 COS_CDN_DOMAIN）")
	cdnWait := flag.Bool("cdn-wait", true, "是否等待 CDN 刷新/预热任务完成（false 则提交后立即返回）")
	flag.Func("note", "发行说明，每条一项（可多次），如 -note \"修复6月合计行错误\"",
		func(s string) error {
			notes = append(notes, s)
			return nil
		})
	flag.Func("asset", "产物，格式 GOOS/GOARCH=文件路径（可多次），如 windows/amd64=dist/app.exe",
		func(s string) error {
			i := strings.Index(s, "=")
			if i <= 0 {
				return fmt.Errorf("asset 格式应为 GOOS/GOARCH=路径: %s", s)
			}
			parts := strings.SplitN(s[:i], "/", 2)
			if len(parts) != 2 {
				return fmt.Errorf("asset 平台格式应为 GOOS/GOARCH: %s", s[:i])
			}
			assets = append(assets, cosupdater.PublishAsset{
				GOOS: parts[0], GOARCH: parts[1], FilePath: s[i+1:],
			})
			return nil
		})
	flag.Parse()

	// 缺省值回退到环境变量，保证密码不进命令行历史。
	if *bucket == "" {
		*bucket = os.Getenv("COS_BUCKET")
	}
	if *secretID == "" {
		*secretID = os.Getenv("COS_SECRET_ID")
	}
	if *secretKey == "" {
		*secretKey = os.Getenv("COS_SECRET_KEY")
	}
	if *cdnDomain == "" {
		*cdnDomain = os.Getenv("COS_CDN_DOMAIN")
	}

	if *prefix == "" {
		fail("缺少必填参数 -prefix")
	}
	if *version == "" {
		fail("缺少必填参数 -version")
	}
	if len(assets) == 0 {
		fail("至少提供一个 -asset（GOOS/GOARCH=文件路径）")
	}
	if *cdnDomain != "" && (*secretID == "" || *secretKey == "") {
		fail("启用 -cdn-domain 需要同时提供 -secret-id 与 -secret-key")
	}

	cfg := cosupdater.PublishConfig{
		BucketURL:    *bucket,
		Prefix:       *prefix,
		Version:      *version,
		FlatLayout:   *flat,
		ReleaseNotes: notes,
		SecretID:     *secretID,
		SecretKey:    *secretKey,
	}
	ctx := context.Background()
	if err := cosupdater.Publish(ctx, cfg, assets); err != nil {
		fail("%v", err)
	}
	fmt.Printf("✅ 已发布 %s/%s v%s（%d 个产物 + version.json）\n", cfg.BucketURL, cfg.Prefix, cfg.Version, len(assets))

	// CDN 刷新/预热：仅在提供 CDN 域名时启用；失败不阻断发布，仅警告。
	if *cdnDomain != "" {
		if err := cdnAfterPublish(ctx, cfg, assets, *cdnDomain, *cdnWait); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  CDN 刷新/预热未完全生效（本次发布仍成功，稍后可能读到旧缓存）: %v\n", err)
		} else {
			fmt.Printf("🌐 已刷新/预热 CDN（%s）\n", *cdnDomain)
		}
	}
}

// cdnAfterPublish 发布成功后处理 CDN 缓存：
//   - 恒定刷新 version.json（固定路径，需让客户端及时看到新 manifest）；
//   - 默认布局：预热产物（新 URL，无需刷新）；
//   - flat 布局：先刷新产物（清旧缓存，避免客户端 SHA256 校验命中旧文件），再预热。
func cdnAfterPublish(ctx context.Context, cfg cosupdater.PublishConfig, assets []cosupdater.PublishAsset, cdnDomain string, wait bool) error {
	cdnCfg := cosupdater.CDNConfig{
		Domain:    cdnDomain,
		WaitTasks: wait,
		SecretID:  cfg.SecretID,
		SecretKey: cfg.SecretKey,
	}

	var refreshURLs, warmURLs []string
	refreshURLs = append(refreshURLs, cfg.Prefix+"/version.json")
	for _, a := range assets {
		key := publishObjectKey(cfg, a)
		warmURLs = append(warmURLs, key)
		if cfg.FlatLayout {
			refreshURLs = append(refreshURLs, key)
		}
	}

	var errs []string
	if err := cosupdater.RefreshCDN(ctx, cdnCfg, refreshURLs); err != nil {
		errs = append(errs, "刷新: "+err.Error())
	}
	if err := cosupdater.PrewarmCDN(ctx, cdnCfg, warmURLs); err != nil {
		errs = append(errs, "预热: "+err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "；"))
	}
	return nil
}

// publishObjectKey 计算单个产物在桶内的对象键，与库内 assetKey 布局一致：
// 默认 {prefix}/{version}/{GOOS}/{GOARCH}/{文件名}；flat 布局去除版本段。
func publishObjectKey(cfg cosupdater.PublishConfig, a cosupdater.PublishAsset) string {
	base := filepath.Base(a.FilePath)
	parts := []string{cfg.Prefix, a.GOOS, a.GOARCH, base}
	if !cfg.FlatLayout {
		parts = append([]string{cfg.Prefix, cfg.Version}, parts[1:]...)
	}
	var out []string
	for _, p := range parts {
		if p = strings.Trim(p, "/"); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "/")
}

// fail 打印错误并以非零码退出。
func fail(format string, args ...interface{}) {
	fmt.Fprintln(os.Stderr, "发布失败: "+fmt.Sprintf(format, args...))
	os.Exit(1)
}
