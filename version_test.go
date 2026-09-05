package cosupdater

import (
	"errors"
	"strings"
	"testing"
)

// hex64 构造一个 64 位十六进制串。
func hex64(s string) string {
	if s == "" {
		s = "aabb"
	}
	out := strings.Repeat(s, 64/len(s))
	if len(out) < 64 {
		out += s[:64-len(out)]
	}
	return out[:64]
}

func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		name    string
		current string
		remote  string
		want    bool
		wantErr bool
	}{
		{"远程更新", "1.0.0", "2.0.0", true, false},
		{"相同", "1.0.0", "1.0.0", false, false},
		{"远程更旧", "2.0.0", "1.9.9", false, false},
		{"远程带 v 前缀", "1.0.0", "v1.1.0", true, false},
		{"当前带 v 前缀", "v1.0.0", "1.1.0", true, false},
		{"两边都带 v 前缀", "v1.0.0", "v2.0.0", true, false},
		{"逐段比较而非字符串比较", "1.9.9", "1.10.0", true, false},
		{"补丁号更新", "1.0.0", "1.0.1", true, false},
		{"预发布版低于正式版", "1.0.0-rc.1", "1.0.0", true, false},
		{"高版本号的预发布版仍高于低正式版", "1.0.0", "1.0.1-rc.1", true, false},
		{"当前版本非法", "", "2.0.0", false, true},
		{"远程版本非法", "1.0.0", "abc", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := isNewerVersion(c.current, c.remote)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，却返回 got=%v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外报错: %v", err)
			}
			if got != c.want {
				t.Fatalf("isNewerVersion(%q, %q) = %v, 期望 %v", c.current, c.remote, got, c.want)
			}
		})
	}
}

func TestParseChecksum(t *testing.T) {
	valid := "sha256:" + hex64("")
	cases := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"合法", valid, nil},
		{"大写十六进制合法", "sha256:" + strings.ToUpper(hex64("")), nil},
		{"前后空白合法", "  " + valid + "\n", nil},
		{"缺失", "", ErrChecksumRequired},
		{"只有前缀", "sha256:", ErrChecksumRequired},
		{"缺少前缀", hex64(""), ErrChecksumRequired},
		{"长度不足", "sha256:aabb", ErrChecksumRequired},
		{"含非十六进制字符", "sha256:" + strings.Repeat("zz", 32), ErrChecksumRequired},
		{"错误算法前缀", "md5:" + hex64(""), ErrChecksumRequired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseChecksum(c.input)
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("期望成功，却报错: %v", err)
				}
				if len(got) != 32 {
					t.Fatalf("期望 32 字节摘要，得到 %d 字节", len(got))
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("期望 %v，得到 %v", c.wantErr, err)
			}
		})
	}
}

func TestAssetForPlatform(t *testing.T) {
	info := &VersionInfo{
		Version: "2.0.0",
		Platforms: map[string]*PlatformAsset{
			"windows/amd64": {DownloadURL: "app.exe", Checksum: "sha256:" + hex64("")},
		},
	}

	if _, err := info.AssetForPlatform("windows", "amd64"); err != nil {
		t.Fatalf("存在的平台不应报错: %v", err)
	}
	_, err := info.AssetForPlatform("linux", "arm64")
	if !errors.Is(err, ErrPlatformNotSupported) {
		t.Fatalf("缺失平台期望 ErrPlatformNotSupported，得到 %v", err)
	}
	if _, err = (*VersionInfo)(nil).AssetForPlatform("windows", "amd64"); !errors.Is(err, ErrInvalidVersionInfo) {
		t.Fatalf("nil 接收者期望 ErrInvalidVersionInfo，得到 %v", err)
	}
}
