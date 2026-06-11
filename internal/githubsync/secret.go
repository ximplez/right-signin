package githubsync

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/crypto/nacl/box"
)

type publicKeyResponse struct {
	Key   string `json:"key"`
	KeyID string `json:"key_id"`
}

type secretBody struct {
	EncryptedValue string `json:"encrypted_value"`
	KeyID          string `json:"key_id"`
}

type githubError struct {
	Message string `json:"message"`
	Status  string `json:"status"`
}

func CreateOrUpdateRepoSecret(token, repo, secretName, secretValue string) error {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(repo) == "" || strings.TrimSpace(secretName) == "" || strings.TrimSpace(secretValue) == "" {
		return nil
	}
	key, err := getRepoPublicKey(token, repo)
	if err != nil {
		return err
	}
	encrypted, err := encryptSecret(secretValue, key.Key)
	if err != nil {
		return err
	}
	body, err := json.Marshal(secretBody{EncryptedValue: encrypted, KeyID: key.KeyID})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/actions/secrets/%s", repo, secretName)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	decorateGitHubRequest(req, token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("更新 GitHub Secret 失败: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func getRepoPublicKey(token, repo string) (*publicKeyResponse, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/actions/secrets/public-key", repo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	decorateGitHubRequest(req, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("获取 GitHub 公钥失败: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result publicKeyResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.Key == "" || result.KeyID == "" {
		return nil, fmt.Errorf("GitHub 公钥响应不完整: %s", strings.TrimSpace(string(body)))
	}
	return &result, nil
}

func encryptSecret(secretValue, publicKey string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		return "", err
	}
	if len(decoded) != 32 {
		return "", fmt.Errorf("GitHub 公钥长度异常: %d", len(decoded))
	}
	var key [32]byte
	copy(key[:], decoded)
	encrypted, err := box.SealAnonymous(nil, []byte(secretValue), &key, rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func decorateGitHubRequest(req *http.Request, token string) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "right-signin")
}
