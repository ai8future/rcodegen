package gemini

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/gif"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/image/draw"

	"rcodegen/pkg/runner"
)

// maxImageDim is the max edge length (pixels) sent to the Gemini API.
// Images larger than this are downscaled to avoid safety-filter false positives
// and reduce token count.
const maxImageDim = 1568

// imageModels lists models that generate images and require direct API calls
// because the Gemini CLI does not expose image bytes in any headless output format.
var imageModels = map[string]bool{
	"gemini-3.1-flash-image-preview": true,
}

// ShouldUseDirectAPI returns true for image-generation models.
func (t *Tool) ShouldUseDirectAPI(cfg *runner.Config) bool {
	return imageModels[cfg.Model]
}

// RunDirectAPI calls the Gemini REST API directly, saves any generated images,
// prints the text response, and returns an exit code.
func (t *Tool) RunDirectAPI(cfg *runner.Config, workDir, task string) int {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "GEMINI_API_KEY environment variable not set\n")
		return 1
	}

	out := cfg.Output
	if out == nil {
		out = os.Stdout
	}

	fmt.Fprintf(out, "%s🎨 Calling Gemini image API directly...%s\n", runner.Dim, runner.Reset)

	parts := []map[string]interface{}{
		{"text": task},
	}

	if cfg.ImagePath != "" {
		imgPath := cfg.ImagePath
		if !filepath.IsAbs(imgPath) {
			base := workDir
			if base == "" {
				base, _ = os.Getwd()
			}
			imgPath = filepath.Join(base, imgPath)
		}
		imgPart, info, err := imageFileToPart(imgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to read image %q: %v\n", imgPath, err)
			return 1
		}
		parts = append(parts, imgPart)
		fmt.Fprintf(out, "%s📎 Including input image: %s%s\n", runner.Dim, imgPath, runner.Reset)
		if info != "" {
			fmt.Fprintf(out, "%s   %s%s\n", runner.Dim, info, runner.Reset)
		}
	}

	reqBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{"parts": parts},
		},
		"generationConfig": map[string]interface{}{
			"responseModalities": []string{"IMAGE", "TEXT"},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to marshal request: %v\n", err)
		return 1
	}

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		cfg.Model, apiKey,
	)

	resp, err := http.Post(url, "application/json", bytes.NewReader(bodyBytes)) //nolint:noctx
	if err != nil {
		fmt.Fprintf(os.Stderr, "API request failed: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read response: %v\n", err)
		return 1
	}

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "API error %d: %s\n", resp.StatusCode, string(respBytes))
		return 1
	}

	var apiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text       string `json:"text"`
					InlineData *struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse response: %v\n", err)
		return 1
	}

	savedAny := false
	for _, candidate := range apiResp.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				fmt.Fprintf(out, "%s%s%s\n", runner.White, part.Text, runner.Reset)
			}
			if part.InlineData != nil && part.InlineData.Data != "" {
				path, err := saveImage(part.InlineData.MimeType, part.InlineData.Data, workDir)
				if err != nil {
					fmt.Fprintf(out, "%s🖼  Image save error: %v%s\n", runner.Yellow, err, runner.Reset)
				} else {
					fmt.Fprintf(out, "%s🖼  Image saved:%s %s%s%s\n", runner.Green, runner.Reset, runner.White, path, runner.Reset)
					savedAny = true
				}
			}
		}
	}

	if !savedAny {
		fmt.Fprintf(out, "%sNote: no image data in response (model may have returned text only)%s\n", runner.Dim, runner.Reset)
	}

	return 0
}

// imageFileToPart reads an image file and returns a Gemini API inlineData part.
// If the image exceeds maxImageDim on either axis, it's downscaled and re-encoded
// as JPEG to reduce payload size and avoid safety-filter false positives on large images.
// Returns the part, a short human-readable info string (empty if no downscaling), and any error.
func imageFileToPart(path string) (map[string]interface{}, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}

	mimeType := "image/jpeg"
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".png"):
		mimeType = "image/png"
	case strings.HasSuffix(lower, ".gif"):
		mimeType = "image/gif"
	case strings.HasSuffix(lower, ".webp"):
		mimeType = "image/webp"
	}

	// Try to decode to check dimensions. If decode fails (e.g., unsupported format),
	// fall back to sending the original bytes.
	src, _, decodeErr := image.Decode(bytes.NewReader(data))
	if decodeErr != nil {
		return map[string]interface{}{
			"inlineData": map[string]interface{}{
				"mimeType": mimeType,
				"data":     base64.StdEncoding.EncodeToString(data),
			},
		}, "", nil
	}

	srcBounds := src.Bounds()
	srcW, srcH := srcBounds.Dx(), srcBounds.Dy()

	if srcW <= maxImageDim && srcH <= maxImageDim {
		// No downscaling needed; send original bytes as-is to preserve format.
		return map[string]interface{}{
			"inlineData": map[string]interface{}{
				"mimeType": mimeType,
				"data":     base64.StdEncoding.EncodeToString(data),
			},
		}, "", nil
	}

	// Compute scaled dimensions preserving aspect ratio.
	scale := float64(maxImageDim) / float64(srcW)
	if s := float64(maxImageDim) / float64(srcH); s < scale {
		scale = s
	}
	dstW := int(float64(srcW) * scale)
	dstH := int(float64(srcH) * scale)

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, srcBounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, "", fmt.Errorf("re-encode jpeg: %w", err)
	}

	info := fmt.Sprintf("Downscaled %dx%d → %dx%d (JPEG q85, %d KB)",
		srcW, srcH, dstW, dstH, buf.Len()/1024)

	return map[string]interface{}{
		"inlineData": map[string]interface{}{
			"mimeType": "image/jpeg",
			"data":     base64.StdEncoding.EncodeToString(buf.Bytes()),
		},
	}, info, nil
}

func saveImage(mimeType, b64data, workDir string) (string, error) {
	ext := ".png"
	switch {
	case strings.Contains(mimeType, "jpeg") || strings.Contains(mimeType, "jpg"):
		ext = ".jpg"
	case strings.Contains(mimeType, "gif"):
		ext = ".gif"
	case strings.Contains(mimeType, "webp"):
		ext = ".webp"
	}

	imgBytes, err := base64.StdEncoding.DecodeString(b64data)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	dir := workDir
	if dir == "" {
		dir, _ = os.Getwd()
	}

	filename := "gemini-image-" + time.Now().Format("20060102-150405") + ext
	path := filepath.Join(dir, filename)

	if err := os.WriteFile(path, imgBytes, 0644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	return path, nil
}
