// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/woveterm/wove/pkg/aiusechat/uctypes"
	"github.com/woveterm/wove/pkg/secretstore"
)

const (
	MiniMaxImageEndpoint = "https://api.minimax.io/v1/image_generation"
	MiniMaxImageModel    = "image-01"
	ImageGenTimeout      = 60 * time.Second
)

type imageGenParams struct {
	Prompt      string `json:"prompt"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
}

func parseImageGenInput(input any) (*imageGenParams, error) {
	result := &imageGenParams{}
	if input == nil {
		return nil, fmt.Errorf("input is required")
	}
	inputBytes, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input: %w", err)
	}
	if err := json.Unmarshal(inputBytes, result); err != nil {
		return nil, fmt.Errorf("failed to parse input: %w", err)
	}
	if result.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if result.AspectRatio == "" {
		result.AspectRatio = "16:9"
	}
	return result, nil
}

type miniMaxImageRequest struct {
	Model           string `json:"model"`
	Prompt          string `json:"prompt"`
	AspectRatio     string `json:"aspect_ratio"`
	ResponseFormat  string `json:"response_format"`
	N               int    `json:"n"`
	PromptOptimizer bool   `json:"prompt_optimizer"`
}

type miniMaxImageResponse struct {
	ID   string `json:"id"`
	Data struct {
		ImageURLs []string `json:"image_urls"`
	} `json:"data"`
	Metadata struct {
		SuccessCount int `json:"success_count"`
		FailedCount  int `json:"failed_count"`
	} `json:"metadata"`
	BaseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

func GetGenerateImageToolDefinition() uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:             "generate_image",
		DisplayName:      "Generate Image",
		Description:      "Generate an image from a text description using MiniMax image-01 model. Returns a URL valid for 24h.",
		ShortDescription: "Generate image from text prompt",
		ToolLogName:      "gen:image",
		Strict:           true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "Text description of the image to generate (max 1500 chars)",
				},
				"aspect_ratio": map[string]any{
					"type":        "string",
					"enum":        []string{"1:1", "16:9", "4:3", "3:2", "2:3", "3:4", "9:16", "21:9"},
					"default":     "16:9",
					"description": "Aspect ratio of the generated image",
				},
			},
			"required":             []string{"prompt", "aspect_ratio"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, toolUseData *uctypes.UIMessageDataToolUse) string {
			parsed, err := parseImageGenInput(input)
			if err != nil {
				return fmt.Sprintf("error: %v", err)
			}
			promptPreview := parsed.Prompt
			if len(promptPreview) > 50 {
				promptPreview = promptPreview[:47] + "..."
			}
			return fmt.Sprintf("generating image: %q (%s)", promptPreview, parsed.AspectRatio)
		},
		ToolAnyCallback: func(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
			parsed, err := parseImageGenInput(input)
			if err != nil {
				return nil, err
			}

			// Get MiniMax API key
			apiKey, exists, err := secretstore.GetSecret("minimax_api_key")
			if err != nil || !exists || apiKey == "" {
				return nil, fmt.Errorf("MiniMax API key not configured. Add it via Quick Add Model → MiniMax")
			}

			// Build request
			reqBody := miniMaxImageRequest{
				Model:           MiniMaxImageModel,
				Prompt:          parsed.Prompt,
				AspectRatio:     parsed.AspectRatio,
				ResponseFormat:  "url",
				N:               1,
				PromptOptimizer: true,
			}
			bodyBytes, err := json.Marshal(reqBody)
			if err != nil {
				return nil, fmt.Errorf("failed to build request: %w", err)
			}

			// Call MiniMax Image API
			client := &http.Client{Timeout: ImageGenTimeout}
			req, err := http.NewRequest("POST", MiniMaxImageEndpoint, bytes.NewReader(bodyBytes))
			if err != nil {
				return nil, fmt.Errorf("failed to create request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+apiKey)

			log.Printf("[imagegen] generating image: prompt=%q aspect=%s\n", parsed.Prompt[:min(50, len(parsed.Prompt))], parsed.AspectRatio)

			resp, err := client.Do(req)
			if err != nil {
				return nil, fmt.Errorf("image generation request failed: %w", err)
			}
			defer resp.Body.Close()

			respBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				return nil, fmt.Errorf("failed to read response: %w", err)
			}

			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("image generation failed (HTTP %d): %s", resp.StatusCode, string(respBytes))
			}

			var imgResp miniMaxImageResponse
			if err := json.Unmarshal(respBytes, &imgResp); err != nil {
				return nil, fmt.Errorf("failed to parse response: %w", err)
			}

			if imgResp.BaseResp.StatusCode != 0 {
				return nil, fmt.Errorf("image generation error: %s (code %d)", imgResp.BaseResp.StatusMsg, imgResp.BaseResp.StatusCode)
			}

			if len(imgResp.Data.ImageURLs) == 0 {
				return nil, fmt.Errorf("no images generated")
			}

			imageUrl := imgResp.Data.ImageURLs[0]
			log.Printf("[imagegen] image generated: %s\n", imageUrl[:min(80, len(imageUrl))])

			// Return image URL for frontend display
			// Mark as ephemeral so it's not stored in conversation history
			if toolUseData != nil {
				toolUseData.ImageUrl = imageUrl
			}

			return map[string]any{
				"success":   true,
				"image_url": imageUrl,
				"message":   "Image generated. URL expires in 24 hours — save it if needed.",
			}, nil
		},
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
