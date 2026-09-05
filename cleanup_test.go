package cosupdater

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNextExecutablePath(t *testing.T) {
	cfg := &Config{TargetPath: filepath.Join("D:", "app", "合洋泰软件.exe")}
	cases := []struct{ ver string }{
		{"1.0.3"},
		{"v2.0.0"}, // 前导 v 应被去除，避免 _vv2.0.0
	}
	for _, c := range cases {
		got, err := NextExecutablePath(cfg, &VersionInfo{Version: c.ver})
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join("D:", "app", "合洋泰软件_v"+strings.TrimPrefix(c.ver, "v")+".exe")
		if got != want {
			t.Fatalf("ver=%s 期望 %s，得到 %s", c.ver, want, got)
		}
	}

	// 原扩展名被保留；无扩展名时版本号直接追加。
	got, _ := NextExecutablePath(&Config{TargetPath: filepath.Join("srv", "bin", "tool")}, &VersionInfo{Version: "1.0.0"})
	if want := filepath.Join("srv", "bin", "tool_v1.0.0"); got != want {
		t.Fatalf("无扩展名场景期望 %s，得到 %s", want, got)
	}
}

func TestCleanupOldVersions(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "app_v1.0.2.exe")
	old0 := filepath.Join(dir, "app_v1.0.0.exe")
	old1 := filepath.Join(dir, "app_v1.0.1.exe")
	fixed := filepath.Join(dir, "app.exe")
	for _, p := range []string{cur, old0, old1, fixed} {
		if err := os.WriteFile(p, []byte("bin"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if err := CleanupOldVersions(cur, 2); err != nil {
		t.Fatalf("CleanupOldVersions 报错: %v", err)
	}
	// 保留最近两个（当前 1.0.2 + 上一个 1.0.1），删除更旧的 1.0.0；固定名不动。
	assertFileExists(t, cur, true)
	assertFileExists(t, old1, true)
	assertFileExists(t, old0, false)
	assertFileExists(t, fixed, true)
}

func TestCleanupOldVersionsIgnoresNonSemver(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "app_v2.0.0.exe")
	nonSemver := filepath.Join(dir, "app_vbeta.exe") // 版本不可解析，应跳过
	for _, p := range []string{cur, nonSemver} {
		if err := os.WriteFile(p, []byte("bin"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanupOldVersions(cur, 1); err != nil {
		t.Fatalf("CleanupOldVersions 报错: %v", err)
	}
	assertFileExists(t, nonSemver, true)
	assertFileExists(t, cur, true)
}

func assertFileExists(t *testing.T, p string, want bool) {
	t.Helper()
	_, err := os.Stat(p)
	if (err == nil) != want {
		t.Fatalf("存在性期望 %v（%s），得到 %v", want, p, err == nil)
	}
}

func TestRetire(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "合洋泰销售提成分析工具.exe")
	if err := os.WriteFile(exe, []byte("old-binary"), 0644); err != nil {
		t.Fatal(err)
	}

	old, err := Retire(exe)
	if err != nil {
		t.Fatalf("Retire 报错: %v", err)
	}
	if want := exe + ".old"; old != want {
		t.Fatalf("期望退役路径 %s，得到 %s", want, old)
	}
	// 原文件被改名，.old 存在且内容一致。
	assertFileExists(t, exe, false)
	assertFileExists(t, old, true)
	if b, _ := os.ReadFile(old); string(b) != "old-binary" {
		t.Fatalf("退役文件内容应为 old-binary，得到 %q", string(b))
	}
}

func TestRetireRemovesPreviousOld(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	prevOld := exe + ".old"
	if err := os.WriteFile(exe, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prevOld, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := Retire(exe); err != nil {
		t.Fatalf("Retire 报错: %v", err)
	}
	// 旧的 .old 被清除，改名后的 .old 才是当前退役件。
	if b, _ := os.ReadFile(prevOld); string(b) != "new" {
		t.Fatalf("改名后 .old 内容应为 new，得到 %q", string(b))
	}
	assertFileExists(t, exe, false)
}
