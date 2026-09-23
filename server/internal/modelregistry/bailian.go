package modelregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// BailianAPIOrigin recognizes vendor endpoints, not the configurable provider ID.
// Keep credentials on the configured origin, including workspace and region.
func BailianAPIOrigin(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") || strings.TrimRight(u.Path, "/") != "/compatible-mode/v1" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if !(strings.HasSuffix(host, ".maas.aliyuncs.com") || host == "dashscope.aliyuncs.com" || host == "dashscope-intl.aliyuncs.com" || strings.HasSuffix(host, ".dashscope.aliyuncs.com")) {
		return ""
	}
	u.Path = ""
	return u.String()
}

type DiscoveredModels struct {
	Models      []string `json:"models"`
	Replace     bool     `json:"replace"`
	Checked     int      `json:"checked"`
	Unavailable int      `json:"unavailable"`
	Unverified  int      `json:"unverified"`
}

// DiscoverBailian intersects the compatible catalog with text/tool capabilities,
// then verifies actual inference access. Workspace permission catalogs are not
// marketplace subscription proofs (and the default workspace may report no grants).
// No grants, subscriptions, or tools are executed here. Each probe emits <= 1 token.
func DiscoverBailian(ctx context.Context, p Provider, compatible []string, client *http.Client) (DiscoveredModels, error) {
	result := DiscoveredModels{Models: []string{}, Replace: true}
	origin := BailianAPIOrigin(p.BaseURL)
	if origin == "" {
		return result, errors.New("unsupported Bailian endpoint")
	}
	allowed := map[string]bool{}
	for _, m := range compatible {
		allowed[m] = true
	}
	candidates := []string{}
	seen := map[string]bool{}
	fetched := 0
	for page := 1; page <= 10; page++ {
		endpoint := fmt.Sprintf("%s/api/v1/models?capabilities=TG&features=function-calling&page_size=200&page_no=%d", origin, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return result, errors.New("invalid Bailian catalog endpoint")
		}
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
		resp, err := client.Do(req)
		if err != nil {
			return result, errors.New("Bailian capability discovery failed")
		}
		var body struct {
			Success bool `json:"success"`
			Output  struct {
				Total  int `json:"total"`
				Models []struct {
					Model        string   `json:"model"`
					Features     []string `json:"features"`
					Capabilities []string `json:"capabilities"`
				} `json:"models"`
			} `json:"output"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != 200 || err != nil || !body.Success {
			return result, errors.New("Bailian capability discovery rejected")
		}
		fetched += len(body.Output.Models)
		for _, m := range body.Output.Models {
			if allowed[m.Model] && !seen[m.Model] && slices.Contains(m.Features, "function-calling") && slices.Contains(m.Capabilities, "TG") {
				candidates = append(candidates, m.Model)
				seen[m.Model] = true
			}
		}
		if fetched >= body.Output.Total {
			break
		}
		if len(body.Output.Models) == 0 || page == 10 {
			return result, errors.New("incomplete Bailian capability catalog")
		}
	}
	result.Checked = len(candidates)
	statuses := make([]int, len(candidates))
	jobs := make(chan int, len(candidates))
	for i := range candidates {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	for range min(10, len(candidates)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				statuses[i] = probeBailianAccess(ctx, p, candidates[i], client)
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return result, errors.New("Bailian discovery timed out; existing catalog retained")
	}
	for i, status := range statuses {
		switch status {
		case 1:
			result.Models = append(result.Models, candidates[i])
		case -1:
			result.Unavailable++
		default:
			result.Unverified++
		}
	}
	return result, nil
}

// 1 = inference accepted; -1 = definitive rejection; 0 = transient/unverified.
func probeBailianAccess(ctx context.Context, p Provider, model string, client *http.Client) int {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": "Reply OK."}}, "max_tokens": 1})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	var body struct {
		Choices []json.RawMessage `json:"choices"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body)
	if resp.StatusCode == 200 && err == nil && len(body.Choices) > 0 {
		return 1
	}
	if resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 402 || resp.StatusCode == 403 || resp.StatusCode == 404 {
		return -1
	}
	return 0
}
