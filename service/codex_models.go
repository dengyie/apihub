package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dengyie/apihub/common"
)

const (
	codexLatestReleaseURL      = "https://api.github.com/repos/openai/codex/releases/latest"
	codexClientVersionCacheTTL = time.Hour
)

type codexClientVersionCache struct {
	sync.Mutex
	version   string
	expiresAt time.Time
}

var latestCodexClientVersion codexClientVersionCache

func GetLatestCodexClientVersion(ctx context.Context, client *http.Client) (string, error) {
	return latestCodexClientVersion.get(ctx, client, codexLatestReleaseURL, time.Now())
}

func (cache *codexClientVersionCache) get(ctx context.Context, client *http.Client, releaseURL string, now time.Time) (string, error) {
	cache.Lock()
	defer cache.Unlock()

	if cache.version != "" && now.Before(cache.expiresAt) {
		return cache.version, nil
	}

	version, err := fetchLatestCodexClientVersion(ctx, client, releaseURL)
	if err != nil {
		if cache.version != "" {
			cache.expiresAt = now.Add(codexClientVersionCacheTTL)
			return cache.version, nil
		}
		return "", err
	}

	cache.version = version
	cache.expiresAt = now.Add(codexClientVersionCacheTTL)
	return version, nil
}

func fetchLatestCodexClientVersion(ctx context.Context, client *http.Client, releaseURL string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("nil http client")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "new-api")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("codex release lookup failed: status=%d", resp.StatusCode)
	}

	var release struct {
		Name       string `json:"name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := common.DecodeJson(resp.Body, &release); err != nil {
		return "", err
	}
	if release.Draft || release.Prerelease {
		return "", fmt.Errorf("latest codex release is not stable")
	}
	version := strings.TrimSpace(release.Name)
	if version == "" {
		return "", fmt.Errorf("latest codex release has no version name")
	}
	return version, nil
}

func FetchCodexModels(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	oauthKey *CodexOAuthKey,
	clientVersion string,
) (statusCode int, models []string, err error) {
	if client == nil {
		return 0, nil, fmt.Errorf("nil http client")
	}
	if oauthKey == nil {
		return 0, nil, fmt.Errorf("nil oauth key")
	}

	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	accessToken := strings.TrimSpace(oauthKey.AccessToken)
	accountID := strings.TrimSpace(oauthKey.AccountID)
	clientVersion = strings.TrimSpace(clientVersion)
	if baseURL == "" {
		return 0, nil, fmt.Errorf("empty baseURL")
	}
	if accessToken == "" {
		return 0, nil, fmt.Errorf("codex channel: access_token is required")
	}
	if accountID == "" {
		return 0, nil, fmt.Errorf("codex channel: account_id is required")
	}
	if clientVersion == "" {
		return 0, nil, fmt.Errorf("codex channel: client_version is required")
	}

	modelsURL, err := url.Parse(baseURL + "/backend-api/codex/models")
	if err != nil {
		return 0, nil, err
	}
	query := modelsURL.Query()
	query.Set("client_version", clientVersion)
	modelsURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL.String(), nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("ChatGPT-Account-Id", accountID)
	req.Header.Set("User-Agent", "codex-cli/"+clientVersion)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	body, err := common.ReadUpstreamBody(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if common.IsHtmlOrXmlResponse(resp.Header.Get("Content-Type"), body) {
			return resp.StatusCode, nil, redactCodexSecrets(common.FormatUpstreamResponseError(resp.StatusCode, resp.Header.Get("Content-Type"), body), accessToken, accountID)
		}
		// 401 是鉴权信号而不是失败：调用方 fetchCodexChannelModels 要靠它去刷新
		// OAuth 凭证后重试。Codex 的 401 通常带 {"error":{"message":...}}，若在这里
		// 返回 error，调用方的刷新分支永远走不到，过期凭证就再也自愈不了。
		if resp.StatusCode == http.StatusUnauthorized {
			return resp.StatusCode, nil, nil
		}
		if jsonErr := common.ExtractJsonErrorMessage(body); jsonErr != "" {
			return resp.StatusCode, nil, redactCodexSecrets(fmt.Errorf("上游返回错误 (status code: %d): %s", resp.StatusCode, jsonErr), accessToken, accountID)
		}
		return resp.StatusCode, nil, nil
	}

	if common.IsHtmlOrXmlResponse(resp.Header.Get("Content-Type"), body) {
		return resp.StatusCode, nil, redactCodexSecrets(common.FormatUpstreamResponseError(resp.StatusCode, resp.Header.Get("Content-Type"), body), accessToken, accountID)
	}

	var result struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if err := common.Unmarshal(body, &result); err != nil {
		return resp.StatusCode, nil, redactCodexSecrets(common.FormatUpstreamResponseError(resp.StatusCode, resp.Header.Get("Content-Type"), body), accessToken, accountID)
	}

	seen := make(map[string]struct{}, len(result.Models))
	models = make([]string, 0, len(result.Models))
	for _, item := range result.Models {
		slug := strings.TrimSpace(item.Slug)
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		models = append(models, slug)
	}
	return resp.StatusCode, models, nil
}

// redactCodexSecrets 把凭证从要返回给管理端的错误文案里抹掉。
//
// 这里用的是完整的 OAuth access token（不是被掩码的 API key），而上游的错误页
// 或错误体有时会把请求头原样回显；这些错误文案现在会带上游响应片段，所以每条
// 拼过上游内容的错误都要过一遍。
func redactCodexSecrets(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	redacted := false
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		// 太短的值做全局替换会把正常文本一起改烂（account id 若恰好是 "1"，
		// "status code: 401" 就会变成 "[REDACTED]0[REDACTED]"）。真正的 token
		// 与 account id 都远长于这个阈值。
		if len(secret) < 8 || !strings.Contains(message, secret) {
			continue
		}
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
		redacted = true
	}
	if !redacted {
		return err
	}
	return errors.New(message)
}
