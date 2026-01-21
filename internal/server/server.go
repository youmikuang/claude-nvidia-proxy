package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"claude-nvidia-proxy/internal/config"
	"claude-nvidia-proxy/internal/converter"
	"claude-nvidia-proxy/internal/logging"
	"claude-nvidia-proxy/internal/types"
)

func HandleMessages(w http.ResponseWriter, r *http.Request, cfg *config.ServerConfig) {
	reqID := fmt.Sprintf("req_%d", time.Now().UnixNano())
	if cfg.ServerAPIKey != "" && !checkInboundAuth(r, cfg.ServerAPIKey) {
		log.Printf("[%s] inbound unauthorized", reqID)
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var anthropicReq types.AnthropicMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&anthropicReq); err != nil {
		log.Printf("[%s] invalid inbound json: %v", reqID, err)
		writeJSONError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if strings.TrimSpace(anthropicReq.Model) == "" {
		log.Printf("[%s] missing model", reqID)
		writeJSONError(w, http.StatusBadRequest, "missing_model")
		return
	}
	if anthropicReq.MaxTokens == 0 {
		anthropicReq.MaxTokens = 1024
	}

	openaiReq, err := converter.ConvertAnthropicToOpenAI(&anthropicReq)
	if err != nil {
		log.Printf("[%s] request conversion failed: %v", reqID, err)
		writeJSONError(w, http.StatusBadRequest, "request_conversion_failed")
		return
	}

	logging.LogForwardedRequest(reqID, cfg, anthropicReq, openaiReq)

	if anthropicReq.Stream {
		if err := proxyStream(w, r, cfg, reqID, openaiReq); err != nil {
			log.Printf("[%s] stream proxy error: %v", reqID, err)
		}
		return
	}

	openaiRespBody, resp, err := doUpstreamJSON(r.Context(), cfg, openaiReq)
	if err != nil {
		log.Printf("[%s] upstream request failed: %v", reqID, err)
		writeJSONError(w, http.StatusBadGateway, "upstream_request_failed")
		return
	}
	defer resp.Body.Close()
	log.Printf("[%s] upstream status=%d", reqID, resp.StatusCode)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(openaiRespBody)
		logging.LogForwardedUpstreamBody(reqID, cfg, openaiRespBody)
		return
	}

	var openaiResp types.OpenAIChatCompletionResponse
	if err := json.Unmarshal(openaiRespBody, &openaiResp); err != nil {
		log.Printf("[%s] invalid upstream json: %v", reqID, err)
		logging.LogForwardedUpstreamBody(reqID, cfg, openaiRespBody)
		writeJSONError(w, http.StatusBadGateway, "invalid_upstream_json")
		return
	}
	anthropicResp := converter.ConvertOpenAIToAnthropic(openaiResp)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(anthropicResp)
}

func checkInboundAuth(r *http.Request, expected string) bool {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		got := strings.TrimSpace(auth[len("bearer "):])
		return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
	}
	if got := strings.TrimSpace(r.Header.Get("x-api-key")); got != "" {
		return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
	}
	return false
}

func writeJSONError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"type":    "proxy_error",
			"code":    code,
			"message": code,
		},
	})
}

func doUpstreamJSON(ctx context.Context, cfg *config.ServerConfig, openaiReq types.OpenAIChatCompletionRequest) ([]byte, *http.Response, error) {
	bodyBytes, err := json.Marshal(openaiReq)
	if err != nil {
		return nil, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.UpstreamURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.ProviderAPIKey)

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		_ = resp.Body.Close()
		return nil, nil, err
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	return respBody, resp, nil
}

func proxyStream(w http.ResponseWriter, r *http.Request, cfg *config.ServerConfig, reqID string, openaiReq types.OpenAIChatCompletionRequest) error {
	openaiReq.Stream = true

	bodyBytes, err := json.Marshal(openaiReq)
	if err != nil {
		return err
	}
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, cfg.UpstreamURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Authorization", "Bearer "+cfg.ProviderAPIKey)

	client := &http.Client{Timeout: 0}
	upResp, err := client.Do(upReq)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_request_failed")
		return err
	}
	defer upResp.Body.Close()

	log.Printf("[%s] upstream status=%d (stream)", reqID, upResp.StatusCode)
	if upResp.StatusCode < 200 || upResp.StatusCode >= 300 {
		raw, _ := io.ReadAll(upResp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(upResp.StatusCode)
		_, _ = w.Write(raw)
		logging.LogForwardedUpstreamBody(reqID, cfg, raw)
		return fmt.Errorf("upstream status %d", upResp.StatusCode)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming_not_supported")
		return errors.New("http.Flusher not supported")
	}

	encoder := func(event string, payload any) error {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(b)); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	messageID := fmt.Sprintf("msg_%d", time.Now().UnixMilli())
	_ = encoder("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            messageID,
			"type":          "message",
			"role":          "assistant",
			"model":         openaiReq.Model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	})

	reader := bufio.NewReader(upResp.Body)
	chunkCount := 0
	textChars := 0
	toolDeltaChunks := 0
	toolArgsChars := 0
	var finishReason string
	var preview strings.Builder
	sawDone := false
	type toolState struct {
		contentBlockIndex int
		id                string
		name              string
	}
	toolStates := map[int]*toolState{}

	nextContentBlockIndex := 0
	currentContentBlockIndex := -1
	currentBlockType := ""
	hasTextBlock := false

	assignContentBlockIndex := func() int {
		idx := nextContentBlockIndex
		nextContentBlockIndex++
		return idx
	}

	closeCurrentBlock := func() {
		if currentContentBlockIndex >= 0 {
			_ = encoder("content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": currentContentBlockIndex,
			})
			currentContentBlockIndex = -1
			currentBlockType = ""
		}
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			sawDone = true
			break
		}

		var chunk types.OpenAIChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		chunkCount++
		delta := chunk.Choices[0].Delta

		if len(delta.ToolCalls) > 0 {
			for _, tc := range delta.ToolCalls {
				toolDeltaChunks++
				toolIndex := tc.Index
				if toolIndex < 0 {
					toolIndex = 0
				}
				state := toolStates[toolIndex]

				tcID := strings.TrimSpace(tc.ID)
				if tcID == "" {
					tcID = fmt.Sprintf("call_%d_%d", time.Now().UnixMilli(), toolIndex)
				}
				tcName := strings.TrimSpace(tc.Function.Name)
				if tcName == "" {
					tcName = fmt.Sprintf("tool_%d", toolIndex)
				}

				if state == nil {
					closeCurrentBlock()
					idx := assignContentBlockIndex()
					state = &toolState{contentBlockIndex: idx, id: tcID, name: tcName}
					toolStates[toolIndex] = state

					_ = encoder("content_block_start", map[string]any{
						"type":  "content_block_start",
						"index": idx,
						"content_block": map[string]any{
							"type":  "tool_use",
							"id":    state.id,
							"name":  state.name,
							"input": map[string]any{},
						},
					})
					currentContentBlockIndex = idx
					currentBlockType = "tool_use"
				} else {
					if state.id == "" && tcID != "" {
						state.id = tcID
					}
					if state.name == "" && tcName != "" {
						state.name = tcName
					}
					currentContentBlockIndex = state.contentBlockIndex
					currentBlockType = "tool_use"
				}

				argsPart := tc.Function.Arguments
				if argsPart != "" {
					toolArgsChars += len([]rune(argsPart))
					_ = encoder("content_block_delta", map[string]any{
						"type":  "content_block_delta",
						"index": state.contentBlockIndex,
						"delta": map[string]any{
							"type":         "input_json_delta",
							"partial_json": argsPart,
						},
					})
				}
			}
		}

		if delta.Content != nil && *delta.Content != "" {
			textChars += len([]rune(*delta.Content))
			if cfg.LogStreamPreviewMax > 0 && preview.Len() < cfg.LogStreamPreviewMax {
				preview.WriteString(logging.TakeFirstRunes(*delta.Content, cfg.LogStreamPreviewMax-preview.Len()))
			}
			if currentBlockType != "" && currentBlockType != "text" {
				closeCurrentBlock()
			}
			if !hasTextBlock {
				hasTextBlock = true
				idx := assignContentBlockIndex()
				_ = encoder("content_block_start", map[string]any{
					"type":  "content_block_start",
					"index": idx,
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				})
				currentContentBlockIndex = idx
				currentBlockType = "text"
			}
			_ = encoder("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": currentContentBlockIndex,
				"delta": map[string]any{
					"type": "text_delta",
					"text": *delta.Content,
				},
			})
		}

		if chunk.Choices[0].FinishReason != nil {
			finishReason = *chunk.Choices[0].FinishReason
			stopReason := converter.MapFinishReason(*chunk.Choices[0].FinishReason)
			_ = encoder("message_delta", map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason":   stopReason,
					"stop_sequence": nil,
				},
				"usage": map[string]any{
					"input_tokens":            0,
					"output_tokens":           0,
					"cache_read_input_tokens": 0,
				},
			})
		}
	}

	closeCurrentBlock()

	if finishReason == "" {
		_ = encoder("message_delta", map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   "end_turn",
				"stop_sequence": nil,
			},
			"usage": map[string]any{
				"input_tokens":            0,
				"output_tokens":           0,
				"cache_read_input_tokens": 0,
			},
		})
	}

	_ = encoder("message_stop", map[string]any{
		"type": "message_stop",
	})
	if cfg.LogStreamPreviewMax > 0 {
		log.Printf("[%s] stream summary chunks=%d text_chars=%d tool_delta_chunks=%d tool_args_chars=%d finish_reason=%q saw_done=%v preview=%q", reqID, chunkCount, textChars, toolDeltaChunks, toolArgsChars, finishReason, sawDone, preview.String())
	} else {
		log.Printf("[%s] stream summary chunks=%d text_chars=%d tool_delta_chunks=%d tool_args_chars=%d finish_reason=%q saw_done=%v", reqID, chunkCount, textChars, toolDeltaChunks, toolArgsChars, finishReason, sawDone)
	}
	return nil
}
