package cosupdater

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// progressChunkSize 进度回调的触发粒度：每下载 64KB 回调一次。
const progressChunkSize = 64 * 1024

// checkClient 版本文件检查专用：版本文件很小，允许 30 秒总超时。
var checkClient = &http.Client{Timeout: 30 * time.Second}

// downloadClient 下载专用：不设总超时（大文件在慢网络下下载耗时不可预估，
// 由调用方的 ctx 控制取消），仅限制响应头等待时间。
var downloadClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

// fetchFile 读取一个小文件（版本文件）并返回其全部内容。
func fetchFile(ctx context.Context, fileURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构造版本文件请求失败: %w", err)
	}
	resp, err := checkClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("读取版本文件失败: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// 版本文件很小，1MB 上限足够且能防止误配到超大对象。
	case http.StatusNotFound:
		return nil, fmt.Errorf("版本文件不存在（请确认对象路径与桶权限）")
	default:
		return nil, fmt.Errorf("读取版本文件失败：HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取版本文件失败: %w", err)
	}
	return data, nil
}

// downloadToFile 把 fileURL 的内容下载写入 dst，并按 64KB 粒度触发 onProgress。
// 下载结束时会补发一次回调，保证进度最终能到达 100%。
// ctx 取消时中断下载并返回 ctx 的错误。
func downloadToFile(ctx context.Context, fileURL string, dst io.Writer, onProgress func(downloaded, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return fmt.Errorf("构造下载请求失败: %w", err)
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}

	total := int64(0)
	if resp.ContentLength > 0 {
		total = resp.ContentLength
	}

	buf := make([]byte, progressChunkSize)
	var downloaded int64
	var reported int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return fmt.Errorf("写入临时文件失败: %w", werr)
			}
			downloaded += int64(n)
			if onProgress != nil {
				onProgress(downloaded, total)
				reported = downloaded
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("下载中断: %w", readErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	// 补发收尾回调：总大小已知且尚未报到 100% 时补一次。
	if onProgress != nil && reported != downloaded {
		onProgress(downloaded, total)
	}
	return nil
}
