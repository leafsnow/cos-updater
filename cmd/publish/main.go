// Command publish 是一个通用的 COS 发布工具：上传产物的新版本到给定桶，
// 并生成/上传对应的 version.json。供各程序 build.sh 在构建完成后自动调用。
//
// 用法：
//
//	cosup-publish -prefix AutoPddTax -version 1.0.0 \
//	  -secret-id $COS_SECRET_ID -secret-key $COS_SECRET_KEY \
//	  -bucket https://gxhyt-1303168605.cos.ap-guangzhou.myqcloud.com \
//	  -asset "windows/amd64=dist/拼多多商店自动开票系统.exe" [-note ...] [-flat]
//
// -bucket / 密钥缺省时依次回退到环境变量 COS_BUCKET / COS_SECRET_ID / COS_SECRET_KEY。
// 同一个桶下放多个程序时，每个程序共用桶根地址、仅以不同的 -prefix 区分。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	cosupdater "github.com/leafsnow/cos-updater"
)

var (
	assets []cosupdater.PublishAsset
)

func main() {
	bucket := flag.String("bucket", "", "COS 桶根地址（缺省取环境变量 COS_BUCKET）")
	prefix := flag.String("prefix", "", "程序在桶下的子目录名（如 AutoPddTax），必填")
	version := flag.String("version", "", "本次发布的版本号，必填")
	note := flag.String("note", "", "发行说明（可选）")
	flat := flag.Bool("flat", false, "产物路径不带版本子目录（仅 {prefix}/{GOOS}/{GOARCH}/{文件名}，桶只留最新）")
	secretID := flag.String("secret-id", "", "腾讯云 SecretID（缺省取 COS_SECRET_ID）")
	secretKey := flag.String("secret-key", "", "腾讯云 SecretKey（缺省取 COS_SECRET_KEY）")
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

	if *prefix == "" {
		fail("缺少必填参数 -prefix")
	}
	if *version == "" {
		fail("缺少必填参数 -version")
	}
	if len(assets) == 0 {
		fail("至少提供一个 -asset（GOOS/GOARCH=文件路径）")
	}

	cfg := cosupdater.PublishConfig{
		BucketURL:    *bucket,
		Prefix:       *prefix,
		Version:      *version,
		FlatLayout:   *flat,
		ReleaseNotes: *note,
		SecretID:     *secretID,
		SecretKey:    *secretKey,
	}
	if err := cosupdater.Publish(context.Background(), cfg, assets); err != nil {
		fail("%v", err)
	}
	fmt.Printf("✅ 已发布 %s/%s v%s（%d 个产物 + version.json）\n", cfg.BucketURL, cfg.Prefix, cfg.Version, len(assets))
}

// fail 打印错误并以非零码退出。
func fail(format string, args ...interface{}) {
	fmt.Fprintln(os.Stderr, "发布失败: "+fmt.Sprintf(format, args...))
	os.Exit(1)
}
