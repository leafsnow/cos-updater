package cosupdater

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newPublishCOS 启动一个接受 PUT（上传）与 GET（读取）的模拟 COS 服务器，
// 记录各对象内容与上传顺序，便于断言「先产物、后 version.json」。
func newPublishCOS(t *testing.T) (string, *publishStore) {
	t.Helper()
	store := &publishStore{objects: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimLeft(r.URL.Path, "/")
		switch r.Method {
		case http.MethodPut:
			data, _ := io.ReadAll(r.Body)
			store.add(key, data)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			data, ok := store.get(key)
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", "0")
			_, _ = w.Write(data)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, store
}

type publishStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	order   []string
}

func (s *publishStore) add(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = data
	s.order = append(s.order, key)
}

func (s *publishStore) get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.objects[key]
	return d, ok
}

func (s *publishStore) orderedKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.order))
	copy(out, s.order)
	return out
}

func writeTemp(t *testing.T, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, content, 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPublishUploadsArtifactsThenManifest(t *testing.T) {
	bucket, store := newPublishCOS(t)
	winExe := writeTemp(t, "合洋泰拼多多对账分析系统.exe", fakeBinary(100))
	linuxBin := writeTemp(t, "app-linux", fakeBinary(80))

	cfg := PublishConfig{
		BucketURL:    bucket,
		Prefix:       "AutoPddTax",
		Version:      "2.0.0",
		ReleaseNotes: "修复合计行错误",
		SecretID:     "id",
		SecretKey:    "key",
	}
	err := Publish(context.Background(), cfg, []PublishAsset{
		{GOOS: "windows", GOARCH: "amd64", FilePath: winExe},
		{GOOS: "linux", GOARCH: "amd64", FilePath: linuxBin},
	})
	if err != nil {
		t.Fatalf("Publish 报错: %v", err)
	}

	// 产物按 Prefix/Version/文件名 落位
	winKey := "AutoPddTax/2.0.0/合洋泰拼多多对账分析系统.exe"
	linuxKey := "AutoPddTax/2.0.0/app-linux"
	if got, ok := store.get(winKey); !ok || string(got) != string(fakeBinary(100)) {
		t.Fatalf("Windows 产物未正确上传")
	}
	if got, ok := store.get(linuxKey); !ok || string(got) != string(fakeBinary(80)) {
		t.Fatalf("Linux 产物未正确上传")
	}

	// version.json 落位且内容正确（version/release_notes/platforms/checksum）
	raw, ok := store.get("AutoPddTax/version.json")
	if !ok {
		t.Fatal("version.json 未上传")
	}
	var manifest VersionInfo
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("version.json 解析失败: %v", err)
	}
	if manifest.Version != "2.0.0" || manifest.ReleaseNotes != "修复合计行错误" {
		t.Fatalf("version/release_notes 不正确: %+v", manifest)
	}
	win := manifest.Platforms["windows/amd64"]
	if win == nil || win.DownloadURL != winKey {
		t.Fatalf("windows 平台条目不正确: %+v", win)
	}
	if win.Checksum != mustChecksum(fakeBinary(100)) {
		t.Fatalf("windows checksum 不正确: %s", win.Checksum)
	}
	if linux := manifest.Platforms["linux/amd64"]; linux == nil || linux.DownloadURL != linuxKey {
		t.Fatalf("linux 平台条目不正确: %+v", linux)
	}
}

func TestPublishUploadOrderProductBeforeManifest(t *testing.T) {
	bucket, store := newPublishCOS(t)
	exe := writeTemp(t, "app.exe", fakeBinary(20))
	cfg := PublishConfig{
		BucketURL: bucket, Prefix: "AutoPddTax", Version: "1.0.0",
		SecretID: "id", SecretKey: "key",
	}
	if err := Publish(context.Background(), cfg, []PublishAsset{
		{GOOS: "windows", GOARCH: "amd64", FilePath: exe},
	}); err != nil {
		t.Fatalf("Publish 报错: %v", err)
	}

	keys := store.orderedKeys()
	if len(keys) != 2 {
		t.Fatalf("期望上传 2 个对象（产物 + version.json），实际 %d: %v", len(keys), keys)
	}
	// 先上传产物，后上传 version.json——避免客户端先读到指向 404 的 download_url
	if keys[0] != "AutoPddTax/1.0.0/app.exe" || keys[1] != "AutoPddTax/version.json" {
		t.Fatalf("上传顺序不正确: %v", keys)
	}
}

func TestPublishErrors(t *testing.T) {
	t.Run("缺少密钥", func(t *testing.T) {
		bucket, _ := newPublishCOS(t)
		exe := writeTemp(t, "app.exe", []byte("x"))
		err := Publish(context.Background(), PublishConfig{
			BucketURL: bucket, Prefix: "p", Version: "1.0.0",
		}, []PublishAsset{{GOOS: "windows", GOARCH: "amd64", FilePath: exe}})
		if !errors.Is(err, ErrSecretPair) {
			t.Fatalf("期望 ErrSecretPair，得到 %v", err)
		}
	})

	t.Run("缺少 prefix", func(t *testing.T) {
		bucket, _ := newPublishCOS(t)
		exe := writeTemp(t, "app.exe", []byte("x"))
		err := Publish(context.Background(), PublishConfig{
			BucketURL: bucket, Version: "1.0.0", SecretID: "i", SecretKey: "k",
		}, []PublishAsset{{GOOS: "windows", GOARCH: "amd64", FilePath: exe}})
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("期望 ErrInvalidConfig，得到 %v", err)
		}
	})

	t.Run("缺少 version", func(t *testing.T) {
		bucket, _ := newPublishCOS(t)
		exe := writeTemp(t, "app.exe", []byte("x"))
		err := Publish(context.Background(), PublishConfig{
			BucketURL: bucket, Prefix: "p", SecretID: "i", SecretKey: "k",
		}, []PublishAsset{{GOOS: "windows", GOARCH: "amd64", FilePath: exe}})
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("期望 ErrInvalidConfig，得到 %v", err)
		}
	})

	t.Run("缺少产物", func(t *testing.T) {
		bucket, _ := newPublishCOS(t)
		err := Publish(context.Background(), PublishConfig{
			BucketURL: bucket, Prefix: "p", Version: "1.0.0", SecretID: "i", SecretKey: "k",
		}, nil)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("期望 ErrInvalidConfig，得到 %v", err)
		}
	})

	t.Run("产物文件不存在", func(t *testing.T) {
		bucket, _ := newPublishCOS(t)
		err := Publish(context.Background(), PublishConfig{
			BucketURL: bucket, Prefix: "p", Version: "1.0.0", SecretID: "i", SecretKey: "k",
		}, []PublishAsset{{GOOS: "windows", GOARCH: "amd64", FilePath: "no-such-file.exe"}})
		if err == nil {
			t.Fatal("产物不存在应报错")
		}
	})

	t.Run("产物缺 GOOS/GOARCH", func(t *testing.T) {
		bucket, _ := newPublishCOS(t)
		exe := writeTemp(t, "app.exe", []byte("x"))
		err := Publish(context.Background(), PublishConfig{
			BucketURL: bucket, Prefix: "p", Version: "1.0.0", SecretID: "i", SecretKey: "k",
		}, []PublishAsset{{FilePath: exe}})
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("期望 ErrInvalidConfig，得到 %v", err)
		}
	})
}
