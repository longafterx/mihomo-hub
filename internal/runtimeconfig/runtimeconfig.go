// Package runtimeconfig 只管理管理员导入的完整 YAML 和 DNS 热加载，不操作设备文件。
package runtimeconfig

import (
	"context"
	"errors"
	"hash/fnv"
	"io"
	"net/netip"
	"strings"
	"time"

	"github.com/longafterx/mihomo-hub/internal/instance"
	"go.yaml.in/yaml/v3"
)

var (
	ErrInvalid        = errors.New("invalid full YAML or DNS settings")
	ErrMissing        = errors.New("import a full YAML for this controller first")
	ErrConflict       = errors.New("runtime config changed; refresh before saving")
	ErrUpstream       = errors.New("controller rejected or did not confirm the full config load")
	ErrSaveAfterApply = errors.New("controller accepted config but hub could not save it")
)

type Record struct {
	YAML          string
	ControllerURL string
	Revision      int64
	UpdatedAtMS   int64
}

type Repository interface {
	Get(context.Context, string) (Record, error)
	Save(context.Context, string, Record, int64) error
}

type Loader interface {
	LoadConfig(context.Context, instance.Instance, string) error
}

// DNS 仅包含页面编辑的字段；合并时保留 nameserver-policy 等其他已有 DNS 配置。
type DNS struct {
	Enable       bool     `json:"enable" yaml:"enable"`
	Listen       string   `json:"listen" yaml:"listen"`
	EnhancedMode string   `json:"enhanced-mode" yaml:"enhanced-mode"`
	Nameserver   []string `json:"nameserver" yaml:"nameserver"`
	Fallback     []string `json:"fallback" yaml:"fallback"`
	FakeIPRange  string   `json:"fake-ip-range" yaml:"fake-ip-range"`
	UseHosts     bool     `json:"use-hosts" yaml:"use-hosts"`
}

type View struct {
	Imported    bool  `json:"imported"`
	Revision    int64 `json:"revision"`
	UpdatedAtMS int64 `json:"updated_at_ms"`
	DNS         DNS   `json:"dns"`
}

type Service struct {
	instances  *instance.Service
	repository Repository
	loader     Loader
	// 有界分片锁串行化同一实例的导入/应用；删除实例不会遗留无限增长的锁表。
	locks [32]chan struct{}
}

func New(instances *instance.Service, repository Repository, loader Loader) *Service {
	s := &Service{instances: instances, repository: repository, loader: loader}
	for i := range s.locks {
		s.locks[i] = make(chan struct{}, 1)
	}
	return s
}

func (s *Service) Get(ctx context.Context, id string) (View, error) {
	item, err := s.instances.Get(id)
	if err != nil {
		return View{}, err
	}
	record, err := s.repository.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	return view(record, item.ControllerURL)
}

func (s *Service) Import(ctx context.Context, id, text string, revision int64) (View, error) {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return View{}, err
	}
	defer unlock()
	item, err := s.instances.Get(id)
	if err != nil {
		return View{}, err
	}
	if _, err := parse(text); err != nil {
		return View{}, err
	}
	previous, err := s.repository.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	if previous.Revision != revision {
		return View{}, ErrConflict
	}
	record := Record{YAML: text, ControllerURL: item.ControllerURL, Revision: revision + 1, UpdatedAtMS: time.Now().UTC().UnixMilli()}
	result, err := view(record, item.ControllerURL)
	if err != nil {
		return View{}, err
	}
	if err := s.repository.Save(ctx, id, record, revision); err != nil {
		return View{}, err
	}
	return result, nil
}

func (s *Service) ApplyDNS(ctx context.Context, id string, dns DNS, revision int64) (View, error) {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return View{}, err
	}
	defer unlock()
	item, generation, err := s.instances.Acquire(id)
	if err != nil {
		return View{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(generation, cancel)
	defer stop()
	if generation.Err() != nil {
		cancel()
	}
	record, err := s.repository.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	if record.YAML == "" || record.ControllerURL != item.ControllerURL {
		return View{}, ErrMissing
	}
	if revision != record.Revision {
		return View{}, ErrConflict
	}
	if !validDNS(dns) {
		return View{}, ErrInvalid
	}
	config, err := parse(record.YAML)
	if err != nil {
		return View{}, err
	}
	dnsMap, _ := config["dns"].(map[string]any)
	if dnsMap == nil {
		dnsMap = map[string]any{}
	}
	// 只替换页面拥有的七个字段，完整配置和其它 DNS 策略继续沿用导入内容。
	dnsMap["enable"], dnsMap["listen"] = dns.Enable, dns.Listen
	dnsMap["enhanced-mode"], dnsMap["nameserver"] = dns.EnhancedMode, dns.Nameserver
	dnsMap["fallback"], dnsMap["use-hosts"] = dns.Fallback, dns.UseHosts
	if dns.FakeIPRange != "" {
		dnsMap["fake-ip-range"] = dns.FakeIPRange
	} else {
		delete(dnsMap, "fake-ip-range")
	}
	config["dns"] = dnsMap
	data, err := yaml.Marshal(config)
	if err != nil {
		return View{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return View{}, err
	}
	if err := s.loader.LoadConfig(ctx, item, string(data)); err != nil {
		return View{}, ErrUpstream
	}
	record.YAML, record.Revision, record.UpdatedAtMS = string(data), revision+1, time.Now().UTC().UnixMilli()
	if err := s.repository.Save(ctx, id, record, revision); err != nil {
		return View{}, ErrSaveAfterApply
	}
	return view(record, item.ControllerURL)
}

func (s *Service) lock(ctx context.Context, id string) (func(), error) {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(id)) // hash.Hash.Write 按契约不会返回错误。
	lock := s.locks[hash.Sum32()%uint32(len(s.locks))]
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func parse(text string) (map[string]any, error) {
	decoder := yaml.NewDecoder(strings.NewReader(text))
	config := map[string]any{}
	if err := decoder.Decode(&config); err != nil || len(config) == 0 {
		return nil, ErrInvalid
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrInvalid
	}
	// 拒绝只有 DNS 的片段，避免全量加载时把已有代理和规则清空。
	_, rules := config["rules"]
	_, proxies := config["proxies"]
	_, providers := config["proxy-providers"]
	if !rules && !proxies && !providers {
		return nil, ErrInvalid
	}
	if raw, exists := config["dns"]; exists {
		if _, ok := raw.(map[string]any); !ok {
			return nil, ErrInvalid
		}
	}
	return config, nil
}

func view(record Record, controllerURL string) (View, error) {
	v := View{Revision: record.Revision, DNS: DNS{EnhancedMode: "fake-ip", Nameserver: []string{}, Fallback: []string{}}}
	if record.YAML == "" || record.ControllerURL != controllerURL {
		return v, nil
	}
	config, err := parse(record.YAML)
	if err != nil {
		return View{}, err
	}
	data, err := yaml.Marshal(config["dns"])
	if err != nil {
		return View{}, ErrInvalid
	}
	if err := yaml.Unmarshal(data, &v.DNS); err != nil {
		return View{}, ErrInvalid
	}
	v.Imported, v.UpdatedAtMS = true, record.UpdatedAtMS
	return v, nil
}

func validDNS(dns DNS) bool {
	if dns.EnhancedMode != "fake-ip" && dns.EnhancedMode != "redir-host" {
		return false
	}
	if dns.Enable && len(dns.Nameserver) == 0 {
		return false
	}
	if len(dns.Nameserver) > 64 || len(dns.Fallback) > 64 {
		return false
	}
	if dns.FakeIPRange != "" {
		if _, err := netip.ParsePrefix(dns.FakeIPRange); err != nil {
			return false
		}
	}
	return true
}
