// Package fakemihomo provides synthetic Controllers exclusively for tests and
// local compatibility demos. It never connects to a real Mihomo.
package fakemihomo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/longafterx/mihomo-hub/tests/fixtures"
)

type Observation struct {
	Method string
	URI    string
	Header http.Header
	Body   []byte
}

type Fake struct {
	Server       *httptest.Server
	Secret       string
	Label        string
	Active       atomic.Int64
	Requests     atomic.Int64
	Observations chan Observation
	mu           sync.Mutex
	selected     string
}

func New(label, secret string) *Fake {
	f := &Fake{Secret: secret, Label: label, Observations: make(chan Observation, 256), selected: "DIRECT"}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *Fake) Close() { f.Server.Close() }

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.Requests.Add(1)
	// 无密钥 Controller 接受不带 Authorization 的请求，避免测试替身误报鉴权失败。
	expectedAuth := ""
	if f.Secret != "" {
		expectedAuth = "Bearer " + f.Secret
	}
	if r.Header.Get("Authorization") != expectedAuth {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "body read failed", http.StatusBadRequest)
		return
	}
	select {
	case f.Observations <- Observation{Method: r.Method, URI: r.RequestURI, Header: r.Header.Clone(), Body: body}:
	default:
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		f.stream(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Mock-Instance", f.Label)
	switch {
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/proxies/"):
		var input struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &input); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.selected = input.Name
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete || r.Method == http.MethodPatch:
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/proxies":
		data, err := fixtures.Files.ReadFile("proxies.json")
		if err != nil {
			panic(err)
		}
		f.mu.Lock()
		selected := f.selected
		f.mu.Unlock()
		if selected != "DIRECT" {
			data = []byte(strings.Replace(string(data), `"now":"DIRECT"`, `"now":"`+selected+`"`, 1))
		}
		w.Write(data)
	case strings.HasSuffix(r.URL.Path, "/healthcheck"):
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(r.URL.Path, "/delay"):
		w.Write([]byte(`{"delay":42}`))
	case r.URL.Path == "/providers/proxies" || r.URL.Path == "/providers/rules":
		w.Write([]byte(`{"providers":{}}`))
	default:
		name := strings.TrimPrefix(r.URL.Path, "/") + ".json"
		data, err := fixtures.Files.ReadFile(name)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"not found"}`))
			return
		}
		w.Write(data)
	}
}

func (f *Fake) stream(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("token") != f.Secret {
		http.Error(w, "invalid websocket token", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, Subprotocols: []string{"mihomo-test"},
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	f.Active.Add(1)
	defer f.Active.Add(-1)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer cancel()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if string(data) == "close-normal" {
				conn.Close(websocket.StatusNormalClosure, "fixture closed")
				return
			}
			if string(data) == "close-abrupt" {
				conn.CloseNow()
				return
			}
			if err := conn.Write(ctx, typ, data); err != nil {
				return
			}
		}
	}()
	defer func() { cancel(); conn.CloseNow(); <-readDone }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var data []byte
		switch r.URL.Path {
		case "/connections":
			data, err = fixtures.Files.ReadFile("connections.json")
		case "/traffic":
			data = []byte(`{"up":128,"down":256}`)
		case "/memory":
			data = []byte(`{"inuse":1048576,"oslimit":0}`)
		case "/logs":
			data, err = json.Marshal(map[string]string{"type": "info", "payload": f.Label + " online"})
		default:
			return
		}
		if err != nil || conn.Write(ctx, websocket.MessageText, data) != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
