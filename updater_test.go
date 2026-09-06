package cosupdater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newFakeCOS 启动一个模拟 COS 的测试服务器：
//   - objects 为桶内对象表（key -> 内容）；
//   - requireSign 为 true 时拒绝缺少 q-signature 参数的请求（模拟私有读桶）。
//
// 返回服务器地址（作为 BucketURL）与对象表（供测试计算校验和）。
func newFakeCOS(t *testing.T, objects map[string][]byte, requireSign bool) (string, map[string][]byte) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if requireSign && r.URL.Query().Get("q-signature") == "" {
			http.Error(w, "signature missing", http.StatusForbidden)
			return
		}
		key := strings.TrimLeft(r.URL.Path, "/")
		body, ok := objects[key]
		if !ok {
			http.Error(w, "no such key", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, objects
}

// mustChecksum 计算内容的 SHA256 并格式化成版本文件约定的 "sha256:hex"。
func mustChecksum(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fakeBinary 构造一个足够跨越多个 64KB 回调粒度的伪二进制。
func fakeBinary(size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func writeVersionFile(objects map[string][]byte, key string, info VersionInfo) {
	data, err := json.Marshal(info)
	if err != nil {
		panic(err)
	}
	objects[key] = data
}

// baseCfg 返回指向假桶的合法配置。
func baseCfg(bucketURL string) *Config {
	return &Config{
		BucketURL:       bucketURL,
		VersionFilePath: "updates/version.json",
		CurrentVersion:  "1.0.0",
	}
}

func TestCheckUpdatePublic(t *testing.T) {
	bin := fakeBinary(200 * 1024)
	objects := map[string][]byte{
		"app_v2.exe": bin,
	}
	writeVersionFile(objects, "updates/version.json", VersionInfo{
		Version:      "2.0.0",
		ReleaseNotes: []string{"修复合计行错误", "优化导出速度"},
		Platforms: map[string]*PlatformAsset{
			runtime.GOOS + "/" + runtime.GOARCH: {DownloadURL: "app_v2.exe", Checksum: mustChecksum(bin)},
		},
	})
	bucket, _ := newFakeCOS(t, objects, false)

	has, info, err := CheckUpdate(context.Background(), baseCfg(bucket))
	if err != nil {
		t.Fatalf("CheckUpdate 报错: %v", err)
	}
	if !has {
		t.Fatal("远程 2.0.0 > 本地 1.0.0，期望 has=true")
	}
	if info.Version != "2.0.0" {
		t.Fatalf("版本信息解析不完整: %+v", info)
	}
	if len(info.ReleaseNotes) != 2 || info.ReleaseNotes[0] != "修复合计行错误" || info.ReleaseNotes[1] != "优化导出速度" {
		t.Fatalf("release_notes 数组不正确: %#v", info.ReleaseNotes)
	}
}

func TestCheckUpdateNoUpdate(t *testing.T) {
	objects := map[string][]byte{
		"updates/version.json": []byte(fmt.Sprintf(
			`{"version":"1.0.0","platforms":{%q:{"download_url":"x","checksum":"%s"}}}`,
			runtime.GOOS+"/"+runtime.GOARCH, mustChecksum([]byte("x")))),
	}
	bucket, _ := newFakeCOS(t, objects, false)

	has, info, err := CheckUpdate(context.Background(), baseCfg(bucket))
	if err != nil {
		t.Fatalf("CheckUpdate 报错: %v", err)
	}
	if has {
		t.Fatal("版本相同，期望 has=false")
	}
	if info == nil || info.Version != "1.0.0" {
		t.Fatalf("has=false 时 info 仍应携带解析结果: %+v", info)
	}
}

func TestCheckUpdateErrors(t *testing.T) {
	platformKey := runtime.GOOS + "/" + runtime.GOARCH

	t.Run("版本文件 404", func(t *testing.T) {
		bucket, _ := newFakeCOS(t, map[string][]byte{}, false)
		_, _, err := CheckUpdate(context.Background(), baseCfg(bucket))
		if err == nil || !strings.Contains(err.Error(), "版本文件不存在") {
			t.Fatalf("期望 404 提示，得到 %v", err)
		}
	})

	t.Run("缺少 platforms", func(t *testing.T) {
		bucket, _ := newFakeCOS(t, map[string][]byte{
			"updates/version.json": []byte(`{"version":"2.0.0"}`),
		}, false)
		_, _, err := CheckUpdate(context.Background(), baseCfg(bucket))
		if !errors.Is(err, ErrInvalidVersionInfo) {
			t.Fatalf("期望 ErrInvalidVersionInfo，得到 %v", err)
		}
	})

	t.Run("JSON 非法", func(t *testing.T) {
		bucket, _ := newFakeCOS(t, map[string][]byte{
			"updates/version.json": []byte(`not-json`),
		}, false)
		_, _, err := CheckUpdate(context.Background(), baseCfg(bucket))
		if !errors.Is(err, ErrInvalidVersionInfo) {
			t.Fatalf("期望 ErrInvalidVersionInfo，得到 %v", err)
		}
	})

	t.Run("配置非法", func(t *testing.T) {
		_, _, err := CheckUpdate(context.Background(), &Config{BucketURL: "myqcloud.com"})
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("期望 ErrInvalidConfig，得到 %v", err)
		}
	})

	t.Run("密钥只给一半", func(t *testing.T) {
		cfg := baseCfg("https://example.com")
		cfg.SecretID = "id"
		if _, _, err := CheckUpdate(context.Background(), cfg); !errors.Is(err, ErrSecretPair) {
			t.Fatalf("期望 ErrSecretPair，得到 %v", err)
		}
	})

	_ = platformKey
}

func TestApplyUpdateFullFlow(t *testing.T) {
	bin := fakeBinary(300*1024 + 7) // 非整块，覆盖收尾补发回调的路径
	target := filepath.Join(t.TempDir(), "合洋泰对账系统.exe")
	if err := os.WriteFile(target, []byte("old-binary"), 0644); err != nil {
		t.Fatal(err)
	}

	objects := map[string][]byte{"app/版本2.exe": bin}
	writeVersionFile(objects, "updates/version.json", VersionInfo{
		Version: "2.0.0",
		Platforms: map[string]*PlatformAsset{
			runtime.GOOS + "/" + runtime.GOARCH: {DownloadURL: "app/版本2.exe", Checksum: mustChecksum(bin)},
		},
	})
	bucket, _ := newFakeCOS(t, objects, false)

	cfg := baseCfg(bucket)
	cfg.TargetPath = target
	cfg.OnProgress = func(downloaded, total int64) {}

	newPath, err := ApplyUpdate(context.Background(), cfg, &VersionInfo{
		Version: "2.0.0",
		Platforms: map[string]*PlatformAsset{
			runtime.GOOS + "/" + runtime.GOARCH: {DownloadURL: "app/版本2.exe", Checksum: mustChecksum(bin)},
		},
	})
	if err != nil {
		t.Fatalf("ApplyUpdate 报错: %v", err)
	}

	// 固定名入口：新版本落位到固定名，程序名不变、不带 _v 版本号。
	if want := filepath.Join(filepath.Dir(target), "合洋泰对账系统.exe"); newPath != want {
		t.Fatalf("新程序路径预期固定名 %s，得到 %s", want, newPath)
	}
	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(bin) {
		t.Fatal("新版本文件内容应与下载内容一致")
	}
	// 旧程序被退役成 <固定名>.old-<时间戳> 备份（可见、可回滚、不隐藏），固定名现在是新版。
	old, err := findOldBackup(t, filepath.Dir(target), filepath.Base(target))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(old); string(b) != "old-binary" {
		t.Fatalf("退役备份内容应为 old-binary，得到 %q", string(b))
	}
	// Windows 上可执行位无意义，Unix 上由库保证 0755。
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(newPath)
		if fi.Mode().Perm() != 0755 {
			t.Fatalf("期望权限 0755，得到 %v", fi.Mode().Perm())
		}
	}
}

func TestApplyUpdateProgressCallback(t *testing.T) {
	bin := fakeBinary(256 * 1024) // 恰好 4 个 64KB 块
	target := filepath.Join(t.TempDir(), "target.bin")
	if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	objects := map[string][]byte{"app.exe": bin}
	bucket, _ := newFakeCOS(t, objects, false)

	var calls int
	var lastDownloaded, lastTotal int64
	cfg := baseCfg(bucket)
	cfg.TargetPath = target
	cfg.OnProgress = func(d, total int64) {
		calls++
		lastDownloaded, lastTotal = d, total
	}

	if _, err := ApplyUpdate(context.Background(), cfg, &VersionInfo{
		Version: "2.0.0",
		Platforms: map[string]*PlatformAsset{
			runtime.GOOS + "/" + runtime.GOARCH: {DownloadURL: "app.exe", Checksum: mustChecksum(bin)},
		},
	}); err != nil {
		t.Fatalf("ApplyUpdate 报错: %v", err)
	}
	if calls < 4 {
		t.Fatalf("256KB 至少应回调 4 次，实际 %d 次", calls)
	}
	if lastDownloaded != int64(len(bin)) || lastTotal != int64(len(bin)) {
		t.Fatalf("最终回调应到达 100%%，得到 %d/%d", lastDownloaded, lastTotal)
	}
}

func TestApplyUpdateFailures(t *testing.T) {
	bin := []byte("good-binary")
	platformKey := runtime.GOOS + "/" + runtime.GOARCH
	otherKey := "other/other"
	if platformKey == otherKey {
		otherKey = "other/other2"
	}

	newInfo := func(url, checksum string) *VersionInfo {
		return &VersionInfo{
			Version: "2.0.0",
			Platforms: map[string]*PlatformAsset{
				platformKey: {DownloadURL: url, Checksum: checksum},
			},
		}
	}

	t.Run("校验和不匹配", func(t *testing.T) {
		objects := map[string][]byte{"app.exe": bin}
		bucket, _ := newFakeCOS(t, objects, false)
		target := filepath.Join(t.TempDir(), "target.bin")
		os.WriteFile(target, []byte("old"), 0644)

		badSum := "sha256:" + strings.Repeat("00", 32)
		cfg := baseCfg(bucket)
		cfg.TargetPath = target
		_, err := ApplyUpdate(context.Background(), cfg, newInfo("app.exe", badSum))
		if !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("期望 ErrChecksumMismatch，得到 %v", err)
		}
		got, _ := os.ReadFile(target)
		if string(got) != "old" {
			t.Fatal("校验失败时原文件必须原样保留")
		}
	})

	t.Run("缺少 checksum", func(t *testing.T) {
		bucket, _ := newFakeCOS(t, map[string][]byte{"app.exe": bin}, false)
		cfg := baseCfg(bucket)
		cfg.TargetPath = filepath.Join(t.TempDir(), "target.bin")
		if _, err := ApplyUpdate(context.Background(), cfg, newInfo("app.exe", "")); !errors.Is(err, ErrChecksumRequired) {
			t.Fatalf("期望 ErrChecksumRequired，得到 %v", err)
		}
	})

	t.Run("当前平台缺失", func(t *testing.T) {
		objects := map[string][]byte{"app.exe": bin}
		writeVersionFile(objects, "updates/version.json", VersionInfo{
			Version:   "2.0.0",
			Platforms: map[string]*PlatformAsset{otherKey: {DownloadURL: "app.exe", Checksum: mustChecksum(bin)}},
		})
		bucket, _ := newFakeCOS(t, objects, false)
		cfg := baseCfg(bucket)
		err := RunUpdate(context.Background(), cfg)
		if !errors.Is(err, ErrPlatformNotSupported) {
			t.Fatalf("期望 ErrPlatformNotSupported，得到 %v", err)
		}
	})

	t.Run("下载对象 404", func(t *testing.T) {
		bucket, _ := newFakeCOS(t, map[string][]byte{}, false)
		cfg := baseCfg(bucket)
		cfg.TargetPath = filepath.Join(t.TempDir(), "target.bin")
		os.WriteFile(cfg.TargetPath, []byte("old"), 0644)
		_, err := ApplyUpdate(context.Background(), cfg, newInfo("missing.exe", mustChecksum(bin)))
		if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
			t.Fatalf("期望 HTTP 404 错误，得到 %v", err)
		}
	})

	t.Run("私有读模式拒绝绝对 URL", func(t *testing.T) {
		cfg := baseCfg("https://example.com")
		cfg.SecretID, cfg.SecretKey = "id", "key"
		cfg.TargetPath = filepath.Join(t.TempDir(), "target.bin")
		os.WriteFile(cfg.TargetPath, []byte("old"), 0644)
		_, err := ApplyUpdate(context.Background(), cfg, newInfo("https://evil.example.com/app.exe", mustChecksum(bin)))
		if !errors.Is(err, ErrInvalidDownloadURL) {
			t.Fatalf("期望 ErrInvalidDownloadURL，得到 %v", err)
		}
	})
}

func TestPrivateBucketSignedRequests(t *testing.T) {
	bin := fakeBinary(130 * 1024)
	objects := map[string][]byte{"apps/中文 版本 v2.exe": bin}
	writeVersionFile(objects, "updates/version.json", VersionInfo{
		Version: "2.0.0",
		Platforms: map[string]*PlatformAsset{
			runtime.GOOS + "/" + runtime.GOARCH: {DownloadURL: "apps/中文 版本 v2.exe", Checksum: mustChecksum(bin)},
		},
	})
	// requireSign=true：请求必须带 q-sign-* 参数（中文与空格转义后仍能命中对象）
	bucket, _ := newFakeCOS(t, objects, true)

	cfg := baseCfg(bucket)
	cfg.SecretID, cfg.SecretKey = "test-id", "test-key"
	cfg.TargetPath = filepath.Join(t.TempDir(), "target.bin")
	if err := os.WriteFile(cfg.TargetPath, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg.OnProgress = func(d, total int64) {}

	has, info, err := CheckUpdate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("私有读 CheckUpdate 报错: %v", err)
	}
	if !has {
		t.Fatal("期望私有读下检查到更新")
	}
	newPath, err := ApplyUpdate(context.Background(), cfg, info)
	if err != nil {
		t.Fatalf("私有读 ApplyUpdate 报错: %v", err)
	}
	got, err := os.ReadFile(newPath)
	if err != nil || string(got) != string(bin) {
		t.Fatalf("私有读更新结果不正确: %v", err)
	}
	// 固定名落位：newPath 即 TargetPath；旧程序被退役成 <固定名>.old-<时间戳> 备份。
	if newPath != cfg.TargetPath {
		t.Fatalf("固定名落位后 newPath 应为 TargetPath，得到 %s", newPath)
	}
	old, err := findOldBackup(t, filepath.Dir(cfg.TargetPath), filepath.Base(cfg.TargetPath))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(old); string(b) != "old" {
		t.Fatalf("退役备份内容应为 old，得到 %q", string(b))
	}
}

func TestDownloadContextCancel(t *testing.T) {
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/big.exe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "104857600")
		for i := 0; i < 100; i++ {
			select {
			case <-release:
				return
			case <-time.After(50 * time.Millisecond):
				w.Write(fakeBinary(1024 * 1024))
			}
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	cfg := baseCfg(srv.URL)
	cfg.TargetPath = filepath.Join(t.TempDir(), "target.bin")
	if err := os.WriteFile(cfg.TargetPath, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := ApplyUpdate(ctx, cfg, &VersionInfo{
		Version: "2.0.0",
		Platforms: map[string]*PlatformAsset{
			runtime.GOOS + "/" + runtime.GOARCH: {DownloadURL: "big.exe", Checksum: mustChecksum(fakeBinary(1))},
		},
	})
	if err == nil {
		t.Fatal("ctx 到期后下载应被中断并返回错误")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("期望 context 超时错误，得到 %v", err)
	}
}

func TestResolveFileURL(t *testing.T) {
	t.Run("公有读拼接并转义", func(t *testing.T) {
		cfg := baseCfg("https://b.cos.ap-guangzhou.myqcloud.com/")
		got, err := resolveFileURL(cfg, "apps/中文 v2.exe")
		if err != nil {
			t.Fatal(err)
		}
		want := "https://b.cos.ap-guangzhou.myqcloud.com/apps/%E4%B8%AD%E6%96%87%20v2.exe"
		if got != want {
			t.Fatalf("得到 %s，期望 %s", got, want)
		}
	})
	t.Run("公有读允许绝对 URL", func(t *testing.T) {
		cfg := baseCfg("https://b.example.com")
		got, err := resolveFileURL(cfg, "https://cdn.example.com/x.exe")
		if err != nil || got != "https://cdn.example.com/x.exe" {
			t.Fatalf("得到 %s, %v", got, err)
		}
	})
	t.Run("尾斜杠去重", func(t *testing.T) {
		cfg := baseCfg("https://b.example.com///")
		got, err := resolveFileURL(cfg, "x.exe")
		if err != nil || got != "https://b.example.com/x.exe" {
			t.Fatalf("得到 %s, %v", got, err)
		}
	})
}

func TestCosObjectURLShape(t *testing.T) {
	now := time.Unix(1700000000, 0)
	raw, err := cosObjectURL("https://b.example.com", "apps/版 本.exe", "id", "key", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "https://b.example.com/apps/") {
		t.Fatalf("URL 形态不对: %s", raw)
	}
	// 按解码后的查询参数做语义断言（";" 会被标准编码为 %3B，属正常）
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("q-sign-algorithm") != "sha1" || q.Get("q-ak") != "id" {
		t.Fatalf("签名参数不完整: %s", raw)
	}
	if q.Get("q-sign-time") != "1700000000;1700000900" || q.Get("q-key-time") != q.Get("q-sign-time") {
		t.Fatalf("签名时间窗不对: %s", raw)
	}
	if len(q.Get("q-signature")) != 40 || q.Get("q-header-list") != "" || q.Get("q-url-param-list") != "" {
		t.Fatalf("签名值或空列表参数不对: %s", raw)
	}
	// 同输入必须得到同签名（确定性）
	again, _ := cosObjectURL("https://b.example.com", "apps/版 本.exe", "id", "key", now)
	if raw != again {
		t.Fatal("相同输入产生了不同签名 URL")
	}
}

func TestRunUpdateNoUpdate(t *testing.T) {
	objects := map[string][]byte{
		"updates/version.json": []byte(fmt.Sprintf(
			`{"version":"0.9.0","platforms":{%q:{"download_url":"x","checksum":"%s"}}}`,
			runtime.GOOS+"/"+runtime.GOARCH, mustChecksum([]byte("x")))),
	}
	bucket, _ := newFakeCOS(t, objects, false)
	if err := RunUpdate(context.Background(), baseCfg(bucket)); err != nil {
		t.Fatalf("无新版本时 RunUpdate 应返回 nil，得到 %v", err)
	}
}
