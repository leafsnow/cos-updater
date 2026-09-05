package cosupdater

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// cosSignTTL 签名的有效期窗口。下载大文件耗时可能较长，
// 取 15 分钟以覆盖慢网络下的下载场景。
const cosSignTTL = 15 * time.Minute

// cosObjectURL 生成对象的访问 URL（GET 下载）。
// 密钥为空时返回公有读直链；否则按腾讯云 COS XML API 的签名算法
// 追加 q-sign-* 查询参数（私有读）。now 由调用方注入以便测试。
func cosObjectURL(bucketURL, objectKey, secretID, secretKey string, now time.Time) (string, error) {
	return cosObjectURLMethod(http.MethodGet, bucketURL, objectKey, secretID, secretKey, now)
}

// cosObjectURLMethod 与 cosObjectURL 相同，但允许指定 HTTP 方法。
// 客户端下载走 GET；发布上传走 PUT（COS 的预签名 URL 会按方法校验签名，
// 二者共享同一套 q-sign-* 计算，仅 httpString 中的方法行不同）。
func cosObjectURLMethod(method, bucketURL, objectKey, secretID, secretKey string, now time.Time) (string, error) {
	// rawKey：URL 解码后的原始路径，用于签名；escapedKey：URL 编码后的路径，用于请求 URL。
	rawKey := strings.TrimLeft(objectKey, "/")
	escapedKey := escapeCosKey(rawKey)
	fileURL := strings.TrimRight(bucketURL, "/") + "/" + escapedKey
	switch {
	case secretID == "" && secretKey == "":
		return fileURL, nil // 公有读，直链即可
	case secretID == "" || secretKey == "":
		return "", ErrSecretPair
	}

	keyTime := fmt.Sprintf("%d;%d", now.Unix(), now.Add(cosSignTTL).Unix())
	signKey := hmacSha1Hex(secretKey, keyTime)
	// 未对任何 header 与 query 参数签名，故两个列表均为空；
	// 签名对象仅覆盖方法、对象路径与时间戳。COS 签名要求方法用小写。
	// UriPathname 必须用 URL 解码后的原始字节 rawKey（含中文/空格用原文），
	// 而非 URL 编码后的 escapedKey——否则非 ASCII 对象键的 sha1(HttpString) 与
	// COS 用解码字节算出的不一致，会得到 403 SignatureDoesNotMatch。
	httpString := strings.ToLower(method) + "\n/" + rawKey + "\n\n\n"
	stringToSign := "sha1\n" + keyTime + "\n" + sha1Hex(httpString) + "\n"
	signature := hmacSha1Hex(signKey, stringToSign)

	q := url.Values{}
	q.Set("q-sign-algorithm", "sha1")
	q.Set("q-ak", secretID)
	q.Set("q-sign-time", keyTime)
	q.Set("q-key-time", keyTime)
	q.Set("q-header-list", "")
	q.Set("q-url-param-list", "")
	q.Set("q-signature", signature)
	return fileURL + "?" + q.Encode(), nil
}

// escapeCosKey 按路径语义转义对象键：保留 "/" 分隔符，
// 每段做严格的 RFC 3986 转义（保留字母数字与 -_.~），与 COS 签名规范一致。
func escapeCosKey(key string) string {
	segments := strings.Split(key, "/")
	for i, seg := range segments {
		segments[i] = cosEscape(seg)
	}
	return strings.Join(segments, "/")
}

// cosEscape 转义单个路径段。
func cosEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// sha1Hex 返回 sha1 摘要的小写十六进制串。
func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// hmacSha1Hex 返回 HMAC-SHA1 的小写十六进制串。
func hmacSha1Hex(key, s string) string {
	mac := hmac.New(sha1.New, []byte(key))
	mac.Write([]byte(s))
	return hex.EncodeToString(mac.Sum(nil))
}
