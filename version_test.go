package cosupdater

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestVersionInfoUnmarshalArray 校验新版字符串数组的 release_notes 能正确解析成 []string。
func TestVersionInfoUnmarshalArray(t *testing.T) {
	raw := `{"version":"2.0.0","release_notes":["修复6月合计行错误","优化导出速度"],"platforms":{}}`
	var info VersionInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("数组 release_notes 解析失败: %v", err)
	}
	want := []string{"修复6月合计行错误", "优化导出速度"}
	if !reflect.DeepEqual(info.ReleaseNotes, want) {
		t.Fatalf("期望 %#v，得到 %#v", want, info.ReleaseNotes)
	}
}

// TestVersionInfoUnmarshalLegacyString 校验旧版单个字符串的 release_notes 仍能被容错读取，
// 包裹成单元素数组——保证桶上尚未重新发布的旧 version.json 不导致整体解析失败。
func TestVersionInfoUnmarshalLegacyString(t *testing.T) {
	raw := `{"version":"1.0.9","release_notes":"Commission v1.0.9 自动发布","platforms":{}}`
	var info VersionInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("旧版字符串 release_notes 解析失败: %v", err)
	}
	want := []string{"Commission v1.0.9 自动发布"}
	if !reflect.DeepEqual(info.ReleaseNotes, want) {
		t.Fatalf("期望 %#v，得到 %#v", want, info.ReleaseNotes)
	}
}

// TestVersionInfoUnmarshalNotesAbsent 校验缺少 release_notes 字段时得到 nil（不报错）。
func TestVersionInfoUnmarshalNotesAbsent(t *testing.T) {
	raw := `{"version":"2.0.0","platforms":{}}`
	var info VersionInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("缺少 release_notes 解析失败: %v", err)
	}
	if info.ReleaseNotes != nil {
		t.Fatalf("期望 nil，得到 %#v", info.ReleaseNotes)
	}
}

// TestVersionInfoMarshalArray 校验序列化输出数组格式（而非旧版字符串）。
func TestVersionInfoMarshalArray(t *testing.T) {
	info := VersionInfo{
		Version:      "2.0.0",
		ReleaseNotes: []string{"修复6月合计行错误", "优化导出速度"},
		Platforms:    map[string]*PlatformAsset{},
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if _, ok := m["release_notes"].([]interface{}); !ok {
		t.Fatalf("release_notes 应为 JSON 数组，实际为 %T: %s", m["release_notes"], data)
	}
	if got := m["release_notes"].([]interface{}); len(got) != 2 || got[0] != "修复6月合计行错误" {
		t.Fatalf("release_notes 数组内容不正确: %#v", got)
	}
}

// TestVersionInfoMarshalOmitEmptyNotes 校验 nil release_notes 时字段被省略（omitempty）。
func TestVersionInfoMarshalOmitEmptyNotes(t *testing.T) {
	info := VersionInfo{
		Version:   "2.0.0",
		Platforms: map[string]*PlatformAsset{},
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if strings.Contains(string(data), "release_notes") {
		t.Fatalf("空 release_notes 应被省略: %s", data)
	}
}
