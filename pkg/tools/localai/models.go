package localai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

func (t *Tool) ListAvailableModels(ctx context.Context) ([]string, error) {
	path := "/api/tags"
	if t.flavor == FlavorLMStudio {
		path = "/v1/models"
	}
	req, cancel, err := t.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer cancel()
	resp, err := directHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list %s models: %w", t.Name(), err)
	}
	defer resp.Body.Close()
	body, err := readBoundedResponse(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list %s models: HTTP %d", t.Name(), resp.StatusCode)
	}

	var names []string
	if t.flavor == FlavorOllama {
		var payload struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("list ollama models: malformed JSON")
		}
		for _, model := range payload.Models {
			names = append(names, model.Name)
		}
	} else {
		var payload struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("list lmstudio models: malformed JSON")
		}
		for _, model := range payload.Data {
			names = append(names, model.ID)
		}
	}

	seen := make(map[string]struct{}, len(names))
	result := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}
