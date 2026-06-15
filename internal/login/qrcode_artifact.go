package login

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"right-signin/internal/browser"
)

func (s *QQAuthenticator) fetchQRCode(ctx context.Context, sess *browser.Session, refreshCount int) (QRCodeInfo, error) {
	if err := sess.SleepRandom(500*time.Millisecond, 1200*time.Millisecond); err != nil {
		return QRCodeInfo{}, err
	}
	raw, err := sess.FindImageSrcByKeywords([]string{"ptqrshow", "qrcode", "qrshow", "qr"})
	if err != nil {
		return QRCodeInfo{}, err
	}
	currentURL, _ := sess.CurrentURL()
	title, _ := sess.Title()
	iframeSrc, _ := sess.Attribute("#ptlogin_iframe", "src")
	iframeURL := ""
	if strings.TrimSpace(iframeSrc) != "" {
		iframeURL = sess.ResolveURL(currentURL, iframeSrc)
	}
	if strings.TrimSpace(raw) == "" {
		if inferred, inferErr := inferQQQRCodeFromLoginURL(currentURL); inferErr == nil && inferred != "" {
			raw = inferred
			log.Printf("已根据当前 QQ 登录页 URL 推导二维码地址，避免切离 OAuth 页面: %s", browser.NormalizeSnippet(raw, 180))
		}
	}
	if strings.TrimSpace(raw) == "" && iframeURL != "" {
		if inferred, inferErr := inferQQQRCodeFromLoginURL(iframeURL); inferErr == nil && inferred != "" {
			raw = inferred
			log.Printf("已根据 iframe 登录 URL 推导二维码地址，避免浏览器切离 OAuth 父页面: %s", browser.NormalizeSnippet(raw, 180))
		} else {
			iframeRaw, fetchErr := s.fetchQRCodeFromIframeURL(iframeURL)
			if fetchErr != nil {
				return QRCodeInfo{}, fetchErr
			}
			raw = iframeRaw
			log.Printf("已通过 iframe 页面源码提取二维码，避免浏览器切离 OAuth 父页面: %s", browser.NormalizeSnippet(raw, 180))
		}
	}
	if strings.TrimSpace(raw) == "" {
		return QRCodeInfo{}, fmt.Errorf("当前页面及 iframe 源中均未找到二维码")
	}
	info := QRCodeInfo{Raw: raw, Kind: detectQRCodeKind(raw), CurrentURL: currentURL, CurrentPage: title, IframeSrc: iframeSrc, IframeURL: iframeURL}
	info.PreviewURL = s.buildPreviewURL(raw, info.Kind)
	pageShot, imagePath, metaPath, err := s.persistQRCodeArtifacts(sess, info, refreshCount)
	if err != nil {
		log.Printf("保存二维码产物失败: %v", err)
	}
	displayPath := s.persistRenderedQRCode(sess, pageShot, refreshCount)
	if displayPath != "" {
		if hash, hashErr := fileSHA256(displayPath); hashErr != nil {
			log.Printf("计算二维码裁剪图哈希失败: %v", hashErr)
		} else {
			info.DisplayHash = hash
		}
		if imageURL, uploadErr := s.uploadOAuthScreenshot(ctx, displayPath, refreshCount); uploadErr != nil {
			log.Printf("上传二维码裁剪图到 OpenList 失败: %v", uploadErr)
		} else if imageURL != "" {
			info.ImageURL = imageURL
			info.ViewerURL = imageURL
		}
		info.DisplayPath = displayPath
	} else {
		log.Printf("未生成二维码裁剪图，本次不再上传 OAuth 全页截图")
	}
	if imagePath != "" {
		if hash, hashErr := fileSHA256(imagePath); hashErr != nil {
			log.Printf("计算二维码原图哈希失败: %v", hashErr)
		} else {
			info.ImageHash = hash
		}
	}
	info.PageShot = pageShot
	info.ImagePath = imagePath
	info.MetaPath = metaPath
	return info, nil
}

func (s *QQAuthenticator) isQRCodeInvalid(sess *browser.Session) (bool, string, error) {
	body, err := sess.BodyText()
	if err != nil {
		return false, "", err
	}
	for _, kw := range []string{"二维码已失效", "点击刷新", "登录超时", "请点击刷新"} {
		if strings.Contains(body, kw) {
			return true, "命中关键词: " + kw, nil
		}
	}
	return false, "", nil
}

func (s *QQAuthenticator) refreshQRCode(sess *browser.Session) error {
	log.Printf("检测到二维码失效，尝试刷新")
	if err := sess.ClickFirstByText([]string{"点击刷新", "刷新二维码", "重新加载", "重试"}); err != nil {
		currentURL, currentErr := sess.CurrentURL()
		if currentErr != nil {
			return err
		}
		iframeSrc, iframeErr := sess.Attribute("#ptlogin_iframe", "src")
		if iframeErr == nil && iframeSrc != "" && strings.Contains(currentURL, "graph.qq.com") {
			log.Printf("父级 OAuth 页无法直接点击 iframe 内刷新按钮，改为重载父页面刷新二维码")
			if navErr := sess.Navigate(currentURL); navErr != nil {
				return fmt.Errorf("点击刷新失败且重载父页面失败: %w", navErr)
			}
			return sess.SleepRandom(900*time.Millisecond, 1600*time.Millisecond)
		}
		return err
	}
	return sess.SleepRandom(900*time.Millisecond, 1600*time.Millisecond)
}

func (s *QQAuthenticator) fetchQRCodeFromIframeURL(iframeURL string) (string, error) {
	html, err := s.fetchRemoteHTML(iframeURL)
	if err != nil {
		return "", err
	}
	matches := iframeQRPattern.FindStringSubmatch(html)
	if len(matches) < 2 {
		return "", fmt.Errorf("iframe 页面中未找到二维码图片")
	}
	return resolveMaybeRelativeURL(iframeURL, matches[1]), nil
}

func inferQQQRCodeFromLoginURL(loginURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(loginURL))
	if err != nil {
		return "", err
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("登录 URL 缺少 host")
	}
	query := parsed.Query()
	appid := strings.TrimSpace(query.Get("appid"))
	if appid == "" {
		return "", fmt.Errorf("登录 URL 缺少 appid")
	}
	base := url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: "/ssl/ptqrshow"}
	if base.Scheme == "" {
		base.Scheme = "https"
	}
	qrQuery := url.Values{}
	qrQuery.Set("appid", appid)
	qrQuery.Set("e", firstNonEmpty(query.Get("e"), "2"))
	qrQuery.Set("l", firstNonEmpty(query.Get("l"), "M"))
	qrQuery.Set("s", firstNonEmpty(query.Get("s"), "3"))
	qrQuery.Set("d", firstNonEmpty(query.Get("d"), "72"))
	qrQuery.Set("v", firstNonEmpty(query.Get("v"), "4"))
	if daid := strings.TrimSpace(query.Get("daid")); daid != "" {
		qrQuery.Set("daid", daid)
	}
	if thirdAID := strings.TrimSpace(query.Get("pt_3rd_aid")); thirdAID != "" {
		qrQuery.Set("pt_3rd_aid", thirdAID)
	}
	if target := firstNonEmpty(query.Get("u1"), query.Get("s_url")); strings.TrimSpace(target) != "" {
		qrQuery.Set("u1", target)
	}
	qrQuery.Set("t", strconv.FormatFloat(rand.Float64(), 'f', 16, 64))
	base.RawQuery = qrQuery.Encode()
	return base.String(), nil
}

func (s *QQAuthenticator) fetchRemoteHTML(rawURL string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("抓取 iframe 页面失败，状态码=%d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func resolveMaybeRelativeURL(baseURL, target string) string {
	target = html.UnescapeString(strings.TrimSpace(target))
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "//") {
		parsedBase, err := url.Parse(baseURL)
		if err != nil || parsedBase.Scheme == "" {
			return "https:" + target
		}
		return parsedBase.Scheme + ":" + target
	}
	parsedTarget, err := url.Parse(target)
	if err == nil && parsedTarget.IsAbs() {
		return target
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return target
	}
	ref, err := url.Parse(target)
	if err != nil {
		return target
	}
	return base.ResolveReference(ref).String()
}

func ArtifactBase(artifactDir, prefix string) string {
	return filepath.Join(artifactDir, prefix+"-login")
}

func detectQRCodeKind(raw string) string {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "empty"
	case strings.HasPrefix(raw, "data:image/"):
		return "data-url-image"
	case strings.HasPrefix(raw, "blob:"):
		return "blob-url"
	case strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://"):
		return "remote-image-url"
	default:
		return "unknown"
	}
}

func (s *QQAuthenticator) buildPreviewURL(raw, kind string) string {
	raw = strings.TrimSpace(raw)
	switch kind {
	case "data-url-image":
		return buildImageViewerURL(raw)
	case "remote-image-url":
		return raw
	default:
		return ""
	}
}

func (s *QQAuthenticator) buildImageViewerURLFromFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mimeType := mimeTypeFromImagePath(path)
	dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
	return buildImageViewerURL(dataURL), nil
}

func buildImageViewerURL(dataURL string) string {
	dataURL = strings.TrimSpace(dataURL)
	if dataURL == "" {
		return ""
	}
	return "https://ximplez.github.io/base64-image-viewer/?target=" + url.QueryEscape(dataURL)
}

func (s *QQAuthenticator) persistQRCodeArtifacts(sess *browser.Session, info QRCodeInfo, refreshCount int) (pageShot string, imagePath string, metaPath string, err error) {
	baseName := filepath.Join(s.cfg.RunArtifactDir, fmt.Sprintf("qrcode-%02d", refreshCount))
	pageShot = baseName + "-page.png"
	if err = sess.SaveScreenshot(pageShot); err != nil {
		return pageShot, "", "", err
	}
	if strings.HasPrefix(info.Raw, "data:image/") {
		imagePath = baseName + imageExtFromDataURL(info.Raw)
		if err = writeDataURLImage(info.Raw, imagePath); err != nil {
			log.Printf("写入 data URL 二维码图片失败: %v", err)
			imagePath = ""
		}
	} else if strings.HasPrefix(info.Raw, "http://") || strings.HasPrefix(info.Raw, "https://") {
		imagePath = baseName + imageExtFromRemoteURL(info.Raw)
		if err = s.downloadRemoteImage(info.Raw, imagePath); err != nil {
			log.Printf("下载二维码远程图片失败: %v", err)
			imagePath = ""
		}
	}
	metaPath = baseName + ".json"
	metaBytes, _ := json.MarshalIndent(info, "", "  ")
	if writeErr := os.WriteFile(metaPath, metaBytes, 0o644); writeErr != nil {
		return pageShot, imagePath, metaPath, writeErr
	}
	return pageShot, imagePath, metaPath, nil
}

func (s *QQAuthenticator) persistRenderedQRCode(sess *browser.Session, pageShot string, refreshCount int) string {
	if pageShot == "" {
		return ""
	}
	baseName := filepath.Join(s.cfg.RunArtifactDir, fmt.Sprintf("qrcode-%02d-rendered.png", refreshCount))
	selectors := []string{"#ptlogin_iframe", "iframe#ptlogin_iframe", "iframe[src*='ptlogin2']"}
	for _, selector := range selectors {
		rect, err := sess.ElementRect(selector)
		if err != nil {
			log.Printf("获取二维码容器区域失败 selector=%s: %v", selector, err)
			continue
		}
		if err := cropScreenshotByRect(pageShot, baseName, rect, 180, 120); err == nil {
			return baseName
		}
		log.Printf("按二维码容器区域裁剪截图失败 selector=%s", selector)
	}
	return ""
}

func cropScreenshotByRect(srcPath, dstPath string, rect browser.DOMRect, marginX, marginY float64) error {
	file, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		return err
	}
	bounds := img.Bounds()
	dpr := rect.DPR
	if dpr <= 0 {
		dpr = 1
	}
	x0 := int(math.Floor((rect.X + rect.ScrollX - marginX) * dpr))
	y0 := int(math.Floor((rect.Y + rect.ScrollY - marginY) * dpr))
	x1 := int(math.Ceil((rect.X + rect.ScrollX + rect.Width + marginX) * dpr))
	y1 := int(math.Ceil((rect.Y + rect.ScrollY + rect.Height + marginY) * dpr))
	if x0 < bounds.Min.X {
		x0 = bounds.Min.X
	}
	if y0 < bounds.Min.Y {
		y0 = bounds.Min.Y
	}
	if x1 > bounds.Max.X {
		x1 = bounds.Max.X
	}
	if y1 > bounds.Max.Y {
		y1 = bounds.Max.Y
	}
	if x1 <= x0 || y1 <= y0 {
		return fmt.Errorf("裁剪区域无效")
	}
	cropped := image.NewRGBA(image.Rect(0, 0, x1-x0, y1-y0))
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			cropped.Set(x-x0, y-y0, img.At(x, y))
		}
	}
	out, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer out.Close()
	return png.Encode(out, cropped)
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func writeDataURLImage(raw, path string) error {
	idx := strings.Index(raw, ",")
	if idx <= 0 {
		return fmt.Errorf("非法 data url")
	}
	payload := raw[idx+1:]
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func imageExtFromDataURL(raw string) string {
	switch {
	case strings.HasPrefix(raw, "data:image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(raw, "data:image/webp"):
		return ".webp"
	case strings.HasPrefix(raw, "data:image/gif"):
		return ".gif"
	default:
		return ".png"
	}
}

func imageExtFromRemoteURL(raw string) string {
	lower := strings.ToLower(raw)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp"} {
		if strings.Contains(lower, ext) {
			return ext
		}
	}
	return ".png"
}

func mimeTypeFromImagePath(path string) string {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(path))) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

func (s *QQAuthenticator) downloadRemoteImage(raw, path string) error {
	resp, err := s.httpClient.Get(raw)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("下载二维码图片失败，状态码=%d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
