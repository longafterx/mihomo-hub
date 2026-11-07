package web

import "net/http"

const loginHTML = `<!doctype html>
<html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>MihomoHub 登录</title><link rel="stylesheet" href="/login.css">
<main><span class="eyebrow">MIHOMOHUB</span><h1 id="heading">正在连接控制台</h1><p id="description">正在检查登录状态…</p>
<form id="login" method="post" action="/api/v1/auth/login" aria-busy="true"><p><label>密码 <input name="password" type="password" autocomplete="current-password" aria-describedby="password-hint" required autofocus disabled></label></p>
<p id="password-hint" hidden></p>
<p id="confirmation-field" hidden><label>确认密码 <input name="confirm_password" type="password" autocomplete="new-password" disabled></label></p>
<button type="submit" disabled>请稍候</button><p id="message" role="status" aria-live="polite"></p></form>
<noscript><p>请启用 JavaScript 后设置密码或登录。</p></noscript></main>
<script src="/login.js" defer></script></html>`

const loginJS = `const form = document.getElementById('login');
const button = form.querySelector('button');
const message = document.getElementById('message');
const password = form.elements.namedItem('password');
const confirmation = form.elements.namedItem('confirm_password');
let setupRequired = false;
let minimum = 8;
let maximum = 1024;

// 同一个入口读取服务端状态，首次设置完成后自动恢复为普通密码登录。
async function refreshMode() {
  const response = await fetch('/api/v1/auth/setup', {credentials: 'same-origin', cache: 'no-store'});
  const result = await response.json();
  if (!response.ok) throw new Error('无法读取登录状态，请刷新页面重试。');
  setupRequired = result.data.setup_required;
  minimum = result.data.min_password_bytes;
  maximum = result.data.max_password_bytes;
  document.title = setupRequired ? 'MihomoHub 设置登录密码' : 'MihomoHub 登录';
  document.getElementById('heading').textContent = setupRequired ? '欢迎使用 MihomoHub' : '登录控制台';
  document.getElementById('description').textContent = setupRequired
    ? '首次使用，请设置控制台登录密码。设置完成后即可开始添加实例。'
    : '集中监控多台 Mihomo，查看运行状态与历史流量。';
  const hint = document.getElementById('password-hint');
  hint.hidden = !setupRequired;
  hint.textContent = '至少 ' + minimum + ' 字节，不要求大小写或特殊字符。';
  document.getElementById('confirmation-field').hidden = !setupRequired;
  password.autocomplete = setupRequired ? 'new-password' : 'current-password';
  password.disabled = false;
  confirmation.disabled = !setupRequired;
  confirmation.required = setupRequired;
  form.action = setupRequired ? '/api/v1/auth/setup' : '/api/v1/auth/login';
  button.textContent = setupRequired ? '设置密码并进入' : '登录';
  button.disabled = false;
  form.setAttribute('aria-busy', 'false');
}

form.addEventListener('submit', async event => {
  event.preventDefault();
  if (button.disabled) return;
  button.disabled = true;
  form.setAttribute('aria-busy', 'true');
  message.textContent = '';
  let setupSubmitted = false;
  try {
    const input = {password: password.value};
    if (setupRequired) {
      const length = new TextEncoder().encode(password.value).length;
      if (length < minimum || length > maximum) throw new Error('密码长度需要在 ' + minimum + ' 到 ' + maximum + ' 字节之间。');
      if (password.value !== confirmation.value) throw new Error('两次输入的密码不一致。');
      input.confirm_password = confirmation.value;
    }
    setupSubmitted = setupRequired;
    const response = await fetch(form.action, {
      method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(input)
    });
    const result = await response.json();
    if (!response.ok) {
      const code = result.error?.code;
      if (code === 'already_initialized' || code === 'setup_required') {
        await refreshMode();
        throw new Error(setupRequired ? '请先设置登录密码。' : '密码已设置，请使用现有密码登录。');
      }
      const errors = {
        invalid_credentials: '密码错误，请重新输入。',
        invalid_password: '请检查密码长度和两次输入是否一致。',
        rate_limited: '尝试次数过多，请稍后重试。',
        origin_rejected: '访问地址与服务配置不一致，请检查访问入口。',
        auth_unavailable: '暂时无法完成认证，请稍后重试。',
        audit_unavailable: '暂时无法登录，请稍后重试。'
      };
      throw new Error(errors[code] || '请求失败，请稍后重试。');
    }
    location.assign('/');
  } catch (error) {
    message.textContent = error.message || '连接失败，请稍后重试。';
    // 网络中断可能发生在密码已保存之后，重新读取状态，避免让用户反复设置。
    if (setupSubmitted && setupRequired) {
      try {
        await refreshMode();
        if (!setupRequired) message.textContent = '密码已设置，请使用现有密码登录。';
      } catch { /* 保留原错误，下次提交或刷新时重新检查。 */ }
    }
  } finally {
    password.value = '';
    confirmation.value = '';
    button.disabled = false;
    form.setAttribute('aria-busy', 'false');
    password.focus();
  }
});
refreshMode().catch(() => {
  message.textContent = '无法读取登录状态，请刷新页面重试。';
  form.setAttribute('aria-busy', 'false');
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
