package cosupdater

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 腾讯云 CDN 云 API 3.0（TC3-HMAC-SHA256）相关常量。
// CDN 走 cdn.tencentcloudapi.com，服务名 cdn，版本 2018-06-06。
// cdnEndpoint 用 var 以便测试注入本地 mock 地址。
var (
	cdnEndpoint = "cdn.tencentcloudapi.com"
)

const (
	cdnService    = "cdn"
	cdnAPIVersion = "2018-06-06"

	// cdnBatchSize 每批可提交的 URL 上限。预热接口每次最多 500 条，
	// 刷新接口上限更高，统一按 500 分批以保证安全。
	cdnBatchSize = 500
)

// cdnHTTPClient 用于调用 CDN 云 API。单个请求设较短超时，
// 轮询多次由外层控制节奏，这里只防止单请求长时间悬挂。
var cdnHTTPClient = &http.Client{Timeout: 90 * time.Second}

// CDNConfig 是 CDN 缓存刷新/预热的配置。
//
// 与 COS 的 PublishConfig 不同，CDN 文档区与 COS 桶可同属一个腾讯云账号，
// 但签名用「云 API 3.0」（TC3-HMAC-SHA256），与 COS 的 q-sign-*（HMAC-SHA1）不同，
// 二者共用同一对 SecretID/SecretKey，算法各自独立。
type CDNConfig struct {
	// Domain CDN 访问域名，如 "cdn.example.com" 或 "https://cdn.example.com"。
	// 用于把相对对象键（如 "prefix/2.0.0/win/app.exe"）拼成完整 URL；
	// 若调用方直接传绝对 URL，则 Domain 可省略。
	Domain string

	// WaitTasks 是否等待刷新/预热任务真正完成（默认 false，即提交后立即返回）。
	//
	// true：提交后轮询直到任务结束（成功或失败），确保新内容已生效才返回；
	// false：提交成功拿到 TaskId 即返回（fire-and-forget），适合紧急发布、
	//       不想因 CDN 同步等待拉长发布时长。设为 true 时耗时可能达分钟级。
	WaitTasks bool

	// SecretID 腾讯云 SecretID（复用 COS 账号密钥对）。
	SecretID string

	// SecretKey 腾讯云 SecretKey。
	SecretKey string
}

// confirmCDNAuth 校验 CDN 配置：密钥必须同时提供或同时为空。
func (c *CDNConfig) confirmCDNAuth() error {
	if (c.SecretID == "") != (c.SecretKey == "") {
		return fmt.Errorf("%w（CDN 当前只提供了其中一个）", ErrSecretPair)
	}
	return nil
}

// cdnSubmitResp 是刷新/预热接口的响应壳（{"Response": {...}}）。
type cdnSubmitResp struct {
	Response struct {
		TaskId string `json:"TaskId"`
		Error  *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"Response"`
}

// cdnQueryResp 是查询刷新/预热任务接口的响应壳。
type cdnQueryResp struct {
	Response struct {
		Tasks []struct {
			Status string `json:"Status"`
			Url    string `json:"Url"`
		} `json:"Tasks"`
		Error *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"Response"`
}

// cdnTask 描述一次请求对单个 URL 执行的动作（刷新/预热），供批量路由。
type cdnTask struct {
	Action string // 提交 action："PurgeUrlsCache" 或 "PushUrlsCache"
	Query  string // 查询 action："DescribePurgeTasks" 或 "DescribePushTasks"
	URLs   []string
}

// RefreshCDN 刷新 CDN 缓存：对每个 URL 执行缓存刷新（PurgeUrlsCache），
// 使边缘节点立即回源拉取最新内容。等待任务完成；存在失败任务时返回聚合错误。
//
// urls 可为绝对 http(s) URL，或相对对象键（配合 cfg.Domain 拼接完整 URL）。
func RefreshCDN(ctx context.Context, cfg CDNConfig, urls []string) error {
	return cdnRun(ctx, cfg, cdnTask{
		Action: "PurgeUrlsCache",
		Query:  "DescribePurgeTasks",
		URLs:   urls,
	})
}

// PrewarmCDN 预热 CDN 缓存：对每个 URL 执行缓存预热（PushUrlsCache），
// 提前把源站内容拉进边缘节点，加速客户端首次下载。等待任务完成；
// 存在失败任务时返回聚合错误。
func PrewarmCDN(ctx context.Context, cfg CDNConfig, urls []string) error {
	return cdnRun(ctx, cfg, cdnTask{
		Action: "PushUrlsCache",
		Query:  "DescribePushTasks",
		URLs:   urls,
	})
}

// cdnRun 执行一次刷新/预热：补齐 URL → 分批提交 → 每批等待任务完成 → 汇总错误。
// 默认不阻断：无论个别批次失败与否都尝试完成所有批次，最后聚合。
func cdnRun(ctx context.Context, cfg CDNConfig, task cdnTask) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := cfg.confirmCDNAuth(); err != nil {
		return err
	}

	urls, err := expandCDNURLs(cfg, task.URLs)
	if err != nil {
		return err
	}
	if len(urls) == 0 {
		return fmt.Errorf("%w: CDN 刷新/预热至少需要一个 URL", ErrInvalidConfig)
	}

	var failures []error
	for begin := 0; begin < len(urls); begin += cdnBatchSize {
		end := begin + cdnBatchSize
		if end > len(urls) {
			end = len(urls)
		}
		batch := urls[begin:end]
		if err := cdnSubmitAndWait(ctx, cfg, task, batch); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%w: %v", ErrCDNFailed, failures)
	}
	return nil
}

// cdnSubmitAndWait 对一批 URL 执行提交（并按需等待任务结束）。
// WaitTasks=true 时轮询到任务成功/失败为止；false 时提交成功即返回。
func cdnSubmitAndWait(ctx context.Context, cfg CDNConfig, task cdnTask, urls []string) error {
	taskID, err := cdnSubmit(ctx, cfg, task.Action, urls)
	if err != nil {
		return fmt.Errorf("提交 CDN 刷新/预热失败: %w", err)
	}
	if !cfg.WaitTasks {
		return nil
	}
	return cdnWaitTasks(ctx, cfg, task.Query, taskID)
}

// cdnSubmit 调用云 API 提交刷新/预热，返回本批任务 ID。
func cdnSubmit(ctx context.Context, cfg CDNConfig, action string, urls []string) (string, error) {
	body, err := json.Marshal(map[string]interface{}{"Urls": urls})
	if err != nil {
		return "", fmt.Errorf("序列化 CDN 请求失败: %w", err)
	}
	var out cdnSubmitResp
	rbody, err := cdnDo(ctx, cfg, action, body)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(rbody, &out); err != nil {
		return "", fmt.Errorf("解析 CDN 提交响应失败: %w", err)
	}
	if out.Response.Error != nil {
		return "", fmt.Errorf("CDN %s 返回错误 %s: %s", action, out.Response.Error.Code, out.Response.Error.Message)
	}
	if out.Response.TaskId == "" {
		return "", fmt.Errorf("CDN %s 未返回 TaskId", action)
	}
	return out.Response.TaskId, nil
}

// cdnWaitTasks 轮询查询任务状态直到全部进入终态，或超时 / 被 ctx 取消。
// 终态判定：状态非空且非处理中（processing）。success 视为成功，其它视为失败。
func cdnWaitTasks(ctx context.Context, cfg CDNConfig, queryAction, taskID string) error {
	deadline := time.Now().Add(cdnWaitTimeout)
	for {
		statuses, err := cdnQuery(ctx, cfg, queryAction, taskID)
		if err != nil {
			return fmt.Errorf("查询 CDN 任务失败: %w", err)
		}

		// 查询返回空（任务尚未落到查询面）视为仍在处理，继续等待。
		if len(statuses) > 0 {
			var anyFailed, anyProcessing bool
			for _, st := range statuses {
				s := strings.ToLower(strings.TrimSpace(st))
				switch s {
				case "":
					anyProcessing = true
				case "processing":
					anyProcessing = true
				case "success":
					// 成功
				default:
					anyFailed = true
				}
			}
			if !anyProcessing {
				if anyFailed {
					return fmt.Errorf("CDN 任务 %s 存在失败项: %v", taskID, statuses)
				}
				return nil
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("CDN 任务 %s 等待超时（%s）", taskID, cdnWaitTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cdnPollInterval):
		}
	}
}

// cdnQuery 查询一批任务的状态，返回各自的状态字样。
func cdnQuery(ctx context.Context, cfg CDNConfig, action, taskID string) ([]string, error) {
	body, err := json.Marshal(map[string]interface{}{
		"TaskId": taskID,
		"Offset": 0,
		"Limit":  200,
	})
	if err != nil {
		return nil, fmt.Errorf("序列化 CDN 查询请求失败: %w", err)
	}
	var out cdnQueryResp
	rbody, err := cdnDo(ctx, cfg, action, body)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(rbody, &out); err != nil {
		return nil, fmt.Errorf("解析 CDN 查询响应失败: %w", err)
	}
	if out.Response.Error != nil {
		return nil, fmt.Errorf("CDN %s 返回错误 %s: %s", action, out.Response.Error.Code, out.Response.Error.Message)
	}
	statuses := make([]string, 0, len(out.Response.Tasks))
	for _, t := range out.Response.Tasks {
		statuses = append(statuses, t.Status)
	}
	return statuses, nil
}

// cdnDo 发送一次腾讯云云 API 3.0 请求（POST + TC3-HMAC-SHA256 签名），返回响应原始字节。
func cdnDo(ctx context.Context, cfg CDNConfig, action string, body []byte) ([]byte, error) {
	now := time.Now()
	auth, headers, err := cdnSign(cfg.SecretID, cfg.SecretKey, action, body, now)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+cdnEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("构造 CDN 请求失败: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := cdnHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 CDN %s 失败: %w", action, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 CDN 响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("CDN %s HTTP %d: %s", action, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// cdnSign 计算腾讯云云 API 3.0（TC3-HMAC-SHA256）签名。
// 返回 Authorization 头值与请求所需的其余头。
func cdnSign(secretID, secretKey, action string, body []byte, now time.Time) (string, map[string]string, error) {
	if secretID == "" || secretKey == "" {
		return "", nil, fmt.Errorf("%w: CDN 调用需要 SecretID 与 SecretKey", ErrSecretPair)
	}
	date := now.UTC().Format("2006-01-02")
	timestamp := fmt.Sprintf("%d", now.Unix())

	// CanonicalRequest：方法 / 路径 / 查询串 / 规范头 / 签名头 / 载荷哈希。
	// 仅对 content-type 与 host 两个头签名，header 值须小写且与实际请求一致。
	signedHeaders := "content-type;host"
	canonicalHeaders := "content-type:application/json; charset=utf-8\nhost:" + cdnEndpoint + "\n"
	payloadHash := sha256Hex(body)
	canonicalRequest := strings.Join([]string{
		http.MethodPost,
		"/",
		"",
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	// StringToSign。
	credentialScope := date + "/" + cdnService + "/tc3_request"
	stringToSign := strings.Join([]string{
		"TC3-HMAC-SHA256",
		timestamp,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	// 派生密钥并计算签名。
	secretDate := tc3HmacSHA256([]byte("TC3"+secretKey), []byte(date))
	secretService := tc3HmacSHA256(secretDate, []byte(cdnService))
	secretSigning := tc3HmacSHA256(secretService, []byte("tc3_request"))
	signature := hex.EncodeToString(tc3HmacSHA256(secretSigning, []byte(stringToSign)))

	authorization := "TC3-HMAC-SHA256 Credential=" + secretID + "/" + credentialScope +
		", SignedHeaders=" + signedHeaders + ", Signature=" + signature

	return authorization, map[string]string{
		"X-TC-Action":    action,
		"X-TC-Version":   cdnAPIVersion,
		"X-TC-Timestamp": timestamp,
		"Host":           cdnEndpoint,
	}, nil
}

// expandCDNURLs 把每个 entry 规范成完整 http(s) URL：已是绝对 URL 则原样，
// 否则用 cfg.Domain 拼接成 "https://{domain}/{objectKey}"。
func expandCDNURLs(cfg CDNConfig, entries []string) ([]string, error) {
	domain := strings.TrimRight(strings.TrimSpace(cfg.Domain), "/")
	for _, prefix := range []string{"https://", "http://"} {
		domain = strings.TrimPrefix(domain, prefix)
	}

	urls := make([]string, 0, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if u, err := url.Parse(e); err == nil && u.Host != "" {
			if u.Scheme != "http" && u.Scheme != "https" {
				return nil, fmt.Errorf("%w: CDN URL 须为 http(s): %s", ErrInvalidConfig, e)
			}
			urls = append(urls, e)
			continue
		}
		if domain == "" {
			return nil, fmt.Errorf("%w: 相对对象键 %s 需要 CDN 域名", ErrInvalidConfig, e)
		}
		urls = append(urls, "https://"+domain+"/"+strings.TrimLeft(e, "/"))
	}
	return urls, nil
}

// sha256Hex 返回 sha256 摘要的小写十六进制串。
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// tc3HmacSHA256 计算 HMAC-SHA256。
func tc3HmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

// cdnPollInterval 与 cdnWaitTimeout 控制同步 API 的等待节奏与上限。
var (
	cdnPollInterval = 3 * time.Second
	cdnWaitTimeout  = 10 * time.Minute
)
