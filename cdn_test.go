package cosupdater

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// startCDNMock 启动一个模拟腾讯云 CDN 云 API 的 TLS 服务器，并把全局
// cdnEndpoint / cdnHTTPClient 临时指向它，供测试拦截请求。
// onRequest 按 action 决定返回体：返回 nil 表示不处理（回 400 + Error）。
// 返回的 counters 记录提交/查询调用次数，便于断言。
func startCDNMock(t *testing.T, onRequest func(action string) map[string]interface{}) (*httptest.Server, *cdnCounters) {
	t.Helper()
	c := &cdnCounters{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("X-TC-Version") != cdnAPIVersion {
			t.Errorf("X-TC-Version 应为 %s，得到 %q", cdnAPIVersion, r.Header.Get("X-TC-Version"))
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=") {
			t.Errorf("Authorization 头缺失或格式错误: %q", r.Header.Get("Authorization"))
		}
		action := r.Header.Get("X-TC-Action")
		if action == "PurgeUrlsCache" || action == "PushUrlsCache" {
			c.submitMu.Lock()
			c.submitted++
			c.submitMu.Unlock()
		} else if action == "DescribePurgeTasks" || action == "DescribePushTasks" {
			c.queryMu.Lock()
			c.queried++
			c.queryMu.Unlock()
		}
		resp := onRequest(action)
		w.Header().Set("Content-Type", "application/json")
		if resp == nil {
			w.WriteHeader(http.StatusBadRequest)
			resp = map[string]interface{}{"Response": map[string]interface{}{
				"Error": map[string]interface{}{"Code": "InvalidAction", "Message": "unknown"},
			}}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"Response": resp})
	}))

	oldEndpoint, oldClient := cdnEndpoint, cdnHTTPClient
	cdnEndpoint = srv.Listener.Addr().String()
	cdnHTTPClient = srv.Client()
	t.Cleanup(func() {
		cdnEndpoint, cdnHTTPClient = oldEndpoint, oldClient
		srv.Close()
	})
	return srv, c
}

type cdnCounters struct {
	submitMu sync.Mutex
	submitted int
	queryMu   sync.Mutex
	queried   int
}

// cdnCfg 返回一套带 mock 域名与密钥的 CDNConfig。
func cdnCfg(wait bool) CDNConfig {
	return CDNConfig{Domain: "cdn.example.com", SecretID: "id", SecretKey: "key", WaitTasks: wait}
}

func TestRefreshCDNSuccess(t *testing.T) {
	srv, c := startCDNMock(t, func(action string) map[string]interface{} {
		switch action {
		case "PurgeUrlsCache":
			return map[string]interface{}{"TaskId": "100"}
		case "DescribePurgeTasks":
			return map[string]interface{}{"Tasks": []map[string]string{{"Status": "success"}}}
		}
		return nil
	})
	_ = srv

	if err := RefreshCDN(context.Background(), cdnCfg(true), []string{"AutoPddTax/version.json"}); err != nil {
		t.Fatalf("RefreshCDN 报错: %v", err)
	}
	if c.submitted != 1 || c.queried < 1 {
		t.Fatalf("期望提交 1 次、查询 ≥1 次，实际提交 %d 查询 %d", c.submitted, c.queried)
	}
}

func TestPrewarmCDNSuccess(t *testing.T) {
	_, c := startCDNMock(t, func(action string) map[string]interface{} {
		switch action {
		case "PushUrlsCache":
			return map[string]interface{}{"TaskId": "200"}
		case "DescribePushTasks":
			return map[string]interface{}{"Tasks": []map[string]string{{"Status": "success"}}}
		}
		return nil
	})

	if err := PrewarmCDN(context.Background(), cdnCfg(true), []string{"AutoPddTax/2.0.0/windows/amd64/app.exe"}); err != nil {
		t.Fatalf("PrewarmCDN 报错: %v", err)
	}
	if c.submitted != 1 || c.queried < 1 {
		t.Fatalf("期望提交 1 次、查询 ≥1 次，实际提交 %d 查询 %d", c.submitted, c.queried)
	}
}

// TestCDNTaskFailed：提交成功但任务最终终态为失败，应返回 ErrCDNFailed。
func TestCDNTaskFailed(t *testing.T) {
	_, _ = startCDNMock(t, func(action string) map[string]interface{} {
		switch action {
		case "PurgeUrlsCache":
			return map[string]interface{}{"TaskId": "100"}
		case "DescribePurgeTasks":
			return map[string]interface{}{"Tasks": []map[string]string{{"Status": "failed"}}}
		}
		return nil
	})

	err := RefreshCDN(context.Background(), cdnCfg(true), []string{"AutoPddTax/version.json"})
	if !errors.Is(err, ErrCDNFailed) {
		t.Fatalf("期望 ErrCDNFailed，得到 %v", err)
	}
}

// TestCDNSubmitError：提交阶段接口返回 Error，应包装为 ErrCDNFailed。
func TestCDNSubmitError(t *testing.T) {
	_, _ = startCDNMock(t, func(action string) map[string]interface{} {
		if action == "PurgeUrlsCache" {
			return map[string]interface{}{"Error": map[string]string{"Code": "AuthFailure", "Message": "bad key"}}
		}
		return nil
	})

	err := RefreshCDN(context.Background(), cdnCfg(true), []string{"AutoPddTax/version.json"})
	if !errors.Is(err, ErrCDNFailed) {
		t.Fatalf("期望 ErrCDNFailed，得到 %v", err)
	}
}

// TestCDNNoWait：WaitTasks=false 时只提交、不查询，直接返回。
func TestCDNNoWait(t *testing.T) {
	_, c := startCDNMock(t, func(action string) map[string]interface{} {
		if action == "PushUrlsCache" {
			return map[string]interface{}{"TaskId": "200"}
		}
		return nil
	})

	if err := PrewarmCDN(context.Background(), cdnCfg(false), []string{"AutoPddTax/a.exe"}); err != nil {
		t.Fatalf("PrewarmCDN(fire-and-forget) 报错: %v", err)
	}
	if c.submitted != 1 {
		t.Fatalf("期望提交 1 次，实际 %d", c.submitted)
	}
	if c.queried != 0 {
		t.Fatalf("fire-and-forget 不应发查询，实际查询 %d 次", c.queried)
	}
}

// TestCDNKeyPair：只提供其中一个密钥应返回 ErrSecretPair。
func TestCDNKeyPair(t *testing.T) {
	err := RefreshCDN(context.Background(), CDNConfig{Domain: "cdn.example.com", SecretID: "id"}, []string{"a/b"})
	if !errors.Is(err, ErrSecretPair) {
		t.Fatalf("期望 ErrSecretPair，得到 %v", err)
	}
}

// TestExpandCDNURLs：绝对 URL 原样保留，相对对象键用 Domain 拼接。
func TestExpandCDNURLs(t *testing.T) {
	urls, err := expandCDNURLs(CDNConfig{Domain: "https://cdn.example.com"},
		[]string{"p/2.0.0/win/app.exe", "https://other.com/x.exe", ""})
	if err != nil {
		t.Fatalf("expandCDNURLs 报错: %v", err)
	}
	if len(urls) != 2 {
		t.Fatalf("期望 2 个 URL，得到 %d: %v", len(urls), urls)
	}
	if urls[0] != "https://cdn.example.com/p/2.0.0/win/app.exe" {
		t.Fatalf("相对键拼接错误: %s", urls[0])
	}
	if urls[1] != "https://other.com/x.exe" {
		t.Fatalf("绝对 URL 应原样保留: %s", urls[1])
	}
}

func TestExpandCDNURLsNoDomain(t *testing.T) {
	_, err := expandCDNURLs(CDNConfig{}, []string{"p/app.exe"})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("缺域名应报 ErrInvalidConfig，得到 %v", err)
	}
}

// TestCDNSignStructure：验证 TC3 签名结构与必需头（不含网络调用或腾讯云校验）。
func TestCDNSignStructure(t *testing.T) {
	auth, headers, err := cdnSign("AKIDtest", "secretkey", "PushUrlsCache",
		[]byte(`{"Urls":["https://cdn.example.com/a.exe"]}`), time.Unix(1551113065, 0))
	if err != nil {
		t.Fatalf("cdnSign 报错: %v", err)
	}
	for _, k := range []string{"X-TC-Action", "X-TC-Version", "X-TC-Timestamp", "Host"} {
		if headers[k] == "" {
			t.Fatalf("缺少头 %s", k)
		}
	}
	if headers["X-TC-Action"] != "PushUrlsCache" {
		t.Fatalf("X-TC-Action 错误: %s", headers["X-TC-Action"])
	}
	if !strings.HasPrefix(auth, "TC3-HMAC-SHA256 Credential=AKIDtest/2019-02-25/cdn/tc3_request") {
		t.Fatalf("Credential 作用域错误: %s", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=content-type;host") {
		t.Fatalf("SignedHeaders 错误: %s", auth)
	}
	if !strings.Contains(auth, ", Signature=") {
		t.Fatalf("缺少 Signature: %s", auth)
	}
}
