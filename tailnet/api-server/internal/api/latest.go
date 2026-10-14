package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// The newest client build, for the update check of our RustDesk fork
// (src/tailnet.rs check_software_update). Builds are GitHub releases tagged
// tailnet-b<N>, N being the CI run number the client compares with its own.

var buildTag = regexp.MustCompile(`^tailnet-b(\d+)$`)

type latestInfo struct {
	Build int64  `json:"build"`
	Tag   string `json:"tag,omitempty"`
	URL   string `json:"url,omitempty"`
}

type releases struct {
	url    string // GitHub releases API URL; empty disables the check
	client *http.Client
	ttl    time.Duration

	mu   sync.Mutex
	at   time.Time
	info latestInfo
}

func newReleases(url string) *releases {
	return &releases{url: url, client: &http.Client{Timeout: 10 * time.Second}, ttl: 10 * time.Minute}
}

func (rs *releases) latest(ctx context.Context) (latestInfo, error) {
	if rs == nil || rs.url == "" {
		return latestInfo{}, nil
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if !rs.at.IsZero() && time.Since(rs.at) < rs.ttl {
		return rs.info, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rs.url, nil)
	if err != nil {
		return latestInfo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := rs.client.Do(req)
	if err != nil {
		return latestInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return latestInfo{}, fmt.Errorf("github releases: %s", resp.Status)
	}
	var list []struct {
		Tag   string `json:"tag_name"`
		URL   string `json:"html_url"`
		Draft bool   `json:"draft"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return latestInfo{}, err
	}
	var best latestInfo
	for _, r := range list {
		m := buildTag.FindStringSubmatch(r.Tag)
		if r.Draft || m == nil {
			continue
		}
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && n > best.Build {
			best = latestInfo{Build: n, Tag: r.Tag, URL: r.URL}
		}
	}
	rs.info, rs.at = best, time.Now()
	return best, nil
}

func (s *Server) latestBuild(w http.ResponseWriter, r *http.Request) {
	info, err := s.releases.latest(r.Context())
	if err != nil {
		s.log.Printf("latest build: %v", err)
		writeError(w, http.StatusServiceUnavailable, "release check failed")
		return
	}
	writeJSON(w, http.StatusOK, info)
}
