package web

import "net/http"

const loginHTML = `<!doctype html>
<html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>MihomoHub 登录</title><link rel="stylesheet" href="/login.css">
<main><span class="eyebrow">MIHOMOHUB</span><h1>登录控制台</h1><p>集中监控多台 Mihomo，查看运行状态与历史流量。</p>
<form id="login" method="post" action="/api/v1/auth/login"><p><label>账号 <input name="username" autocomplete="username" required maxlength="128" autofocus></label></p>
<p><label>密码 <input name="password" type="password" autocomplete="current-password" required></label></p>
<button type="submit">登录</button><p id="message" role="status"></p></form></main>
<script src="/login.js" defer></script></html>`

const loginJS = `document.getElementById('login').addEventListener('submit', async event => {
  event.preventDefault();
  const form = event.currentTarget;
  const button = form.querySelector('button');
  const message = document.getElementById('message');
  button.disabled = true;
  try {
    const response = await fetch('/api/v1/auth/login', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({username: form.username.value, password: form.password.value})
    });
    form.password.value = '';
    const result = await response.json();
    if (!response.ok) throw new Error(result.error.message);
    location.assign('/');
  } catch (error) {
    message.textContent = error.message || '登录失败';
  } finally {
    button.disabled = false;
  }
});`

func serveLogin(w http.ResponseWriter, r *http.Request) bool {
	var body, contentType string
	switch r.URL.Path {
	case "/login":
		body, contentType = loginHTML, "text/html; charset=utf-8"
	case "/login.css":
		body, contentType = loginCSS, "text/css; charset=utf-8"
	case "/login.js":
		body, contentType = loginJS, "text/javascript; charset=utf-8"
	default:
		return false
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write([]byte(body))
	}
	return true
}

// 登录页资源与 UI 一起内嵌，CSP 只允许同源脚本和样式。
const loginCSS = `:root{color-scheme:dark;font-family:system-ui,sans-serif;background:#18222b;color:#d9e3eb}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}main{width:min(100%,420px);border-top:3px solid #db9974;padding:32px 0}h1{font-size:28px;letter-spacing:-.03em;margin:14px 0}p{font-size:14px;line-height:1.7;color:#b7c6d2}.eyebrow{font-size:12px;letter-spacing:.18em;color:#db9974}label{display:grid;gap:8px;color:#d9e3eb}input,button{font:inherit;border-radius:8px;min-height:44px;width:100%}input{background:#23313e;color:#edf2f7;border:1px solid #526372;padding:10px 12px}button{border:0;background:#db9974;color:#1a232c;font-weight:650;cursor:pointer;margin-top:12px}button:disabled{opacity:.5}input:focus-visible,button:focus-visible{outline:2px solid #edb78d;outline-offset:3px}#message{min-height:24px;color:#f5b4a9}`
