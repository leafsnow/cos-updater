package cosupdater

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNextExecutablePath(t *testing.T) {
	// 固定名入口：程序名永远不变，不追加 _v 版本号。
	got, err := NextExecutablePath(&Config{TargetPath: filepath.Join("D:", "app", "合洋泰软件.exe")}, &VersionInfo{Version: "1.0.3"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("D:", "app", "合洋泰软件.exe"); got != want {
		t.Fatalf("期望固定名 %s，得到 %s", want, got)
	}

	// 基准名本身带版本号（旧版残留 / 连续更新）：应剥掉，不产生嵌套。
	for _, base := range []string{"合洋泰软件_v1.0.6.exe", "合洋泰软件_v2.0.0.exe"} {
		got, _ := NextExecutablePath(&Config{TargetPath: filepath.Join("D:", "app", base)}, &VersionInfo{Version: "1.0.7"})
		if want := filepath.Join("D:", "app", "合洋泰软件.exe"); got != want {
			t.Fatalf("base=%s 期望固定名 %s，得到 %s", base, want, got)
		}
	}

	// 无扩展名：固定名同样保留原扩展名（此处为空）。
	got, _ = NextExecutablePath(&Config{TargetPath: filepath.Join("srv", "bin", "tool")}, &VersionInfo{Version: "1.0.1"})
	if want := filepath.Join("srv", "bin", "tool"); got != want {
		t.Fatalf("无扩展名场景期望固定名 %s，得到 %s", want, got)
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
	// 退役名应为 <原名>.old-<时间戳>，其中扩展名 .exe 保留，末尾 .old-* 使双击不可运行。
	if !strings.HasPrefix(filepath.Base(old), "合洋泰销售提成分析工具.exe.old-") {
		t.Fatalf("退役名应形如 <原名>.old-<时间戳>，得到 %s", old)
	}
	// 原文件被改名，退役备份存在且内容一致。
	assertFileExists(t, exe, false)
	assertFileExists(t, old, true)
	if b, _ := os.ReadFile(old); string(b) != "old-binary" {
		t.Fatalf("退役文件内容应为 old-binary，得到 %q", string(b))
	}
}

func TestRetireKeepsMultiple(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	if err := os.WriteFile(exe, []byte("first"), 0644); err != nil {
		t.Fatal(err)
	}
	old1, err := Retire(exe)
	if err != nil {
		t.Fatalf("首次 Retire 报错: %v", err)
	}
	// 重新原地放一个"第二版"，再次退役：应生成新备份，且不覆盖第一版。
	if err := os.WriteFile(exe, []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	old2, err := Retire(exe)
	if err != nil {
		t.Fatalf("再次 Retire 报错: %v", err)
	}
	if old1 == old2 {
		t.Fatalf("两次退役应产生不同备份名：%s", old1)
	}
	if b, _ := os.ReadFile(old1); string(b) != "first" {
		t.Fatalf("第一份备份内容应为 first，得到 %q", string(b))
	}
	if b, _ := os.ReadFile(old2); string(b) != "second" {
		t.Fatalf("第二份备份内容应为 second，得到 %q", string(b))
	}
	assertFileExists(t, exe, false)
	assertFileExists(t, old1, true)
	assertFileExists(t, old2, true)
}

// findOldBackup 在 dir 下查找 base 唯一的 ".old-<时间戳>" 备份，返回其路径。
func findOldBackup(t *testing.T, dir, base string) (string, error) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, base+".old-*"))
	if err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("期望恰好一个 .old 备份，得到 %d 个: %v", len(matches), matches)
	}
	return matches[0], nil
}
