package clientapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/geodata"
)

// App 公开路由(对齐上游 v0.1.402/v0.1.412):只校验 routerId,不要求设备 token。
// GET /client/v1/home/:routerId
// GET /client/v1/:routerId/regions
// GET /client/v1/:routerId/background
// GET /client/v1/:routerId/geodata/:tag

// requireRouter 校验路径里的 routerId 是否本机。不对一律 ErrTokenNotFound(404 not found,与上游一致)。
func (s *Service) requireRouter(ctx context.Context, routerID string) error {
	id, err := s.RouterID(ctx)
	if err != nil {
		return err
	}
	if routerID != id {
		return ErrTokenNotFound
	}
	return nil
}

// Home:GET /client/v1/home/:routerId → {ok:true}
func (s *Service) Home(ctx context.Context, routerID string) error {
	return s.requireRouter(ctx, routerID)
}

// PublicBackground 取面板背景图(不要求 token,只要 routerId 对)。
func (s *Service) PublicBackground(ctx context.Context, routerID string) (*BackgroundImage, error) {
	if err := s.requireRouter(ctx, routerID); err != nil {
		return nil, err
	}
	var f struct {
		Image string `json:"image"`
	}
	if _, err := s.Store.Load(backgroundFile, &f); err != nil {
		return nil, ErrTokenNotFound
	}
	typ, data, ok := parseImageDataURL(f.Image)
	if !ok {
		return nil, ErrTokenNotFound
	}
	sum := sha256.Sum256(data)
	return &BackgroundImage{Type: typ, Bytes: data, Version: hex.EncodeToString(sum[:8])}, nil
}

// PublicGeoPath 返回 geodata / 用户规则集 .srs 路径(不要求 token)。
func (s *Service) PublicGeoPath(ctx context.Context, routerID, tag string) (string, error) {
	if err := s.requireRouter(ctx, routerID); err != nil {
		return "", err
	}
	tag = strings.TrimSuffix(tag, ".srs")
	var dir string
	switch {
	case geodata.ValidTag(tag):
		dir = s.GeoDir
	case listTagRe.MatchString(tag):
		dir = s.RulesetDir
	default:
		return "", ErrTokenNotFound
	}
	if dir == "" {
		return "", ErrTokenNotFound
	}
	path := filepath.Join(dir, tag+".srs")
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return "", ErrTokenNotFound
	}
	return path, nil
}

// RegionsPayload 是 GET .../regions 的响应(字段对齐上游公开面)。
type RegionsPayload struct {
	Version      string         `json:"version"`
	Groups       []Group        `json:"groups"`
	Nodes        []regionNode   `json:"nodes"`
	Server       regionServer   `json:"server"`
	Background   *regionBG      `json:"background"`
	TestSites    []any          `json:"testSites"`
	TestSettings map[string]any `json:"testSettings"`
}

type regionNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	G    string `json:"g,omitempty"`
}

type regionServer struct {
	ServerInfo     ServerInfo `json:"serverInfo"`
	DetectedRegion string     `json:"detectedRegion"`
}

type regionBG struct {
	Version     string `json:"version"`
	Transparent int    `json:"transparent,omitempty"`
	Blur        int    `json:"blur,omitempty"`
}

// Regions:GET /client/v1/:routerId/regions
// 规则展开(rules=2)依赖内核/规则库,本实现先回档案里的 groups 原文形状;version 对 groups JSON 摘要。
func (s *Service) Regions(ctx context.Context, routerID string) (RegionsPayload, error) {
	if err := s.requireRouter(ctx, routerID); err != nil {
		return RegionsPayload{}, err
	}
	info, err := s.Info(ctx)
	if err != nil {
		return RegionsPayload{}, err
	}
	raw, _ := json.Marshal(info.ShareRegions)
	sum := sha256.Sum256(raw)
	ver := hex.EncodeToString(sum[:8])

	nodes := []regionNode{}
	if s.Profile != nil {
		prof, err := s.Profile(ctx)
		if err == nil {
			if arr, ok := prof["servers"].([]any); ok {
				var st state
				_, _ = s.Store.Load(stateFile, &st)
				for _, it := range arr {
					m, _ := it.(map[string]any)
					if m == nil {
						continue
					}
					id, _ := m["id"].(string)
					name, _ := m["name"].(string)
					if id == "" {
						continue
					}
					n := regionNode{ID: id, Name: name}
					if st.Shares != nil && st.Shares[id] != nil {
						n.G = st.Shares[id].Gen
					}
					nodes = append(nodes, n)
				}
			}
		}
	}
	var bg *regionBG
	if img, err := s.PublicBackground(ctx, routerID); err == nil {
		bg = &regionBG{Version: img.Version}
	}
	det := ""
	if info.EgressCountry != nil {
		det = info.EgressCountry.Country
	}
	return RegionsPayload{
		Version: ver, Groups: info.ShareRegions, Nodes: nodes,
		Server:     regionServer{ServerInfo: info.ServerInfo, DetectedRegion: det},
		Background: bg, TestSites: []any{}, TestSettings: map[string]any{},
	}, nil
}
