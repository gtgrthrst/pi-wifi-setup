#!/usr/bin/env python3
"""
pi-wifi-setup — 無頭樹莓派 WiFi 設定工具（Python 版）
透過 AP 熱點模式，讓手機或電腦連線後透過網頁介面管理 WiFi 設定。
"""

import argparse
import json
import logging
import os
import random
import signal
import subprocess
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlparse, unquote_plus

# ─── 常數 ────────────────────────────────────────────────────────────────────

AP_CON_NAME = "wifi-setup-ap"
AP_IP       = "10.42.0.1"
STATUS_FILE = "/run/wifi-ap.json"
CONFIG_DIR  = "/etc/wifi-setup"
PASS_FILE   = "/etc/wifi-setup/ap-password"

# ─── 全域狀態 ─────────────────────────────────────────────────────────────────

_ap_status = {"active": False, "ssid": "", "password": "", "webURL": AP_IP}
_ap_lock   = threading.RLock()
_is_auto   = False

# ─── nmcli 解析輔助 ───────────────────────────────────────────────────────────

def split_terse(line: str) -> list[str]:
    """解析 nmcli -t 輸出，處理 \\: 跳脫字元"""
    parts, cur, i = [], [], 0
    while i < len(line):
        if line[i] == '\\' and i + 1 < len(line) and line[i + 1] == ':':
            cur.append(':')
            i += 2
        elif line[i] == ':':
            parts.append(''.join(cur))
            cur = []
            i += 1
        else:
            cur.append(line[i])
            i += 1
    parts.append(''.join(cur))
    return parts


def _run(args: list[str], **kwargs) -> str:
    """執行命令，回傳 stdout 字串；失敗回傳空字串"""
    try:
        return subprocess.check_output(
            args, stderr=subprocess.DEVNULL, **kwargs
        ).decode(errors='replace').strip()
    except Exception:
        return ""


def nm_get(field: str, con_name: str) -> str:
    return _run(["nmcli", "-g", field, "connection", "show", con_name])

# ─── WiFi 連線狀態 ────────────────────────────────────────────────────────────

def is_wifi_connected() -> bool:
    """檢查 wlan0 是否已連線到非 AP 的 WiFi"""
    out = _run(["nmcli", "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device"])
    for line in out.splitlines():
        parts = split_terse(line.strip())
        if len(parts) >= 4:
            _, typ, state, conn = parts[0], parts[1], parts[2], parts[3]
            if typ == "wifi" and state == "connected" and conn != AP_CON_NAME:
                return True
    return False

# ─── 網路管理 ─────────────────────────────────────────────────────────────────

def load_from_nm() -> list[dict]:
    """讀取 NetworkManager 中所有 WiFi 連線"""
    networks = []
    out = _run(["nmcli", "-t", "-f", "NAME,TYPE", "connection", "show"])
    for line in out.splitlines():
        parts = split_terse(line.strip())
        if len(parts) < 2:
            continue
        name, typ = parts[0], parts[1]
        if typ != "802-11-wireless" or name == AP_CON_NAME:
            continue
        ssid = nm_get("802-11-wireless.ssid", name) or name
        psk  = _run(["nmcli", "-s", "-g", "802-11-wireless-security.psk",
                     "connection", "show", name])
        networks.append({"ssid": ssid, "password": psk})
    return networks


def apply_to_nm(ssid: str, password: str) -> None:
    """新增或更新 NetworkManager WiFi 連線"""
    out = _run(["nmcli", "-t", "-f", "NAME,TYPE", "connection", "show"])
    exists = any(
        split_terse(l.strip())[0] == ssid and
        len(split_terse(l.strip())) >= 2 and
        split_terse(l.strip())[1] == "802-11-wireless"
        for l in out.splitlines()
    )
    if exists:
        args = ["nmcli", "connection", "modify", ssid]
        if password:
            args += ["wifi-sec.psk", password]
    else:
        args = [
            "nmcli", "connection", "add", "type", "wifi",
            "ifname", "wlan0", "con-name", ssid, "ssid", ssid,
            "connection.autoconnect", "yes",
            "connection.autoconnect-priority", "10",
        ]
        if password:
            args += ["wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", password]
    subprocess.run(args, stderr=subprocess.DEVNULL)


def delete_from_nm(ssid: str) -> None:
    subprocess.run(["nmcli", "connection", "delete", ssid],
                   stderr=subprocess.DEVNULL)

# ─── WiFi 掃描 ────────────────────────────────────────────────────────────────

def scan_wifi() -> list[dict]:
    """掃描附近 WiFi，回傳依訊號強度排序的清單"""
    cmd_base = ["nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY", "device", "wifi", "list"]
    try:
        out = subprocess.check_output(
            cmd_base + ["--rescan", "yes"],
            timeout=20, stderr=subprocess.DEVNULL
        ).decode(errors='replace')
    except Exception:
        try:
            out = subprocess.check_output(
                cmd_base, timeout=10, stderr=subprocess.DEVNULL
            ).decode(errors='replace')
        except Exception:
            return []

    seen, results = set(), []
    for line in out.splitlines():
        parts = split_terse(line.strip())
        if len(parts) < 2:
            continue
        ssid = parts[0].strip()
        if not ssid or ssid in seen:
            continue
        seen.add(ssid)
        try:
            sig = int(parts[1].strip())
        except ValueError:
            sig = 0
        secure = len(parts) > 2 and parts[2] not in ("", "--")
        results.append({"ssid": ssid, "signal": sig, "secure": secure})

    results.sort(key=lambda x: x["signal"], reverse=True)
    return results

# ─── AP 管理 ──────────────────────────────────────────────────────────────────

def gen_password() -> str:
    chars = "abcdefghjkmnpqrstuvwxyz23456789"
    return ''.join(random.choice(chars) for _ in range(8))


def get_or_create_password() -> str:
    """自動模式使用持久化密碼（重啟後不變）"""
    if os.path.exists(PASS_FILE):
        try:
            p = open(PASS_FILE).read().strip()
            if len(p) >= 8:
                return p
        except Exception:
            pass
    p = gen_password()
    os.makedirs(CONFIG_DIR, mode=0o700, exist_ok=True)
    with open(PASS_FILE, 'w') as f:
        f.write(p)
    os.chmod(PASS_FILE, 0o600)
    return p


def start_ap(ssid: str, password: str) -> None:
    subprocess.run(["nmcli", "connection", "delete", AP_CON_NAME],
                   stderr=subprocess.DEVNULL)
    result = subprocess.run(
        ["nmcli", "device", "wifi", "hotspot",
         "ifname", "wlan0", "con-name", AP_CON_NAME,
         "ssid", ssid, "password", password],
        capture_output=True, text=True
    )
    if result.returncode != 0:
        raise RuntimeError(f"nmcli hotspot 失敗: {result.stderr.strip()}")


def stop_ap() -> None:
    subprocess.run(["nmcli", "connection", "delete", AP_CON_NAME],
                   stderr=subprocess.DEVNULL)


def set_status(active: bool, ssid: str = "", password: str = "") -> None:
    global _ap_status
    data = {"active": active, "ssid": ssid, "password": password, "webURL": AP_IP}
    with _ap_lock:
        _ap_status = data
    try:
        with open(STATUS_FILE, 'w') as f:
            json.dump(data, f)
    except Exception:
        pass


def clear_status() -> None:
    global _ap_status
    with _ap_lock:
        _ap_status = {"active": False, "ssid": "", "password": "", "webURL": AP_IP}
    try:
        with open(STATUS_FILE, 'w') as f:
            f.write('{"active":false}')
    except Exception:
        pass

# ─── HTML 樣板 ────────────────────────────────────────────────────────────────

HTML_TEMPLATE = """\
<!DOCTYPE html>
<html lang="zh-TW">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Wi-Fi &#x8A2D;&#x5B9A;</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
     background:#f0f2f5;color:#1a1a1a;padding:16px;max-width:500px;margin:0 auto}
h1{font-size:1.4rem;font-weight:700;margin-bottom:4px}
.sub{font-size:.82rem;color:#666;margin-bottom:18px}
.card{background:#fff;border-radius:12px;padding:16px;
      box-shadow:0 1px 4px rgba(0,0,0,.08);margin-bottom:14px}
.card h2{font-size:.95rem;font-weight:600;margin-bottom:12px;color:#444;
         display:flex;align-items:center;justify-content:space-between}
.net{display:flex;align-items:center;justify-content:space-between;
     padding:9px 0;border-bottom:1px solid #f0f0f0}
.net:last-child{border-bottom:none}
.net-name{font-weight:500;font-size:.9rem;word-break:break-all}
.net-sub{font-size:.75rem;color:#aaa;margin-top:2px}
.scan-item{display:flex;align-items:center;justify-content:space-between;
           padding:9px 10px;border-radius:8px;cursor:pointer;
           border:1px solid #eee;margin-bottom:6px;background:#fafafa;
           transition:background .15s}
.scan-item:hover{background:#e8f0fe;border-color:#1a73e8}
.scan-ssid{font-weight:500;font-size:.9rem}
.scan-sig{font-size:.78rem;color:#888;white-space:nowrap;margin-left:8px}
.scan-placeholder{color:#aaa;font-size:.85rem;text-align:center;padding:12px 0}
input[type=text],input[type=password]{
  width:100%;padding:10px 12px;border:1px solid #ddd;border-radius:8px;
  font-size:.95rem;margin-bottom:10px;background:#fafafa}
input:focus{outline:none;border-color:#1a73e8;background:#fff}
.btn{padding:9px 16px;border-radius:8px;border:none;cursor:pointer;
     font-size:.88rem;font-weight:500;white-space:nowrap}
.del{background:#fff0f0;color:#e53e3e}
.scan-btn{background:#f0f4ff;color:#1a73e8;flex:none}
.add{background:#1a73e8;color:#fff;width:100%;padding:12px;
     font-size:.95rem;border-radius:8px;border:none;cursor:pointer}
.stop{background:#ff9500;color:#fff;width:100%;padding:12px;
      font-size:.95rem;border-radius:8px;border:none;cursor:pointer}
.badge{font-size:.72rem;padding:2px 8px;border-radius:20px;font-weight:600;margin-left:6px}
.badge-auto{background:#e8f5e9;color:#2e7d32}
.badge-manual{background:#fff3e0;color:#e65100}
.info-row{display:flex;justify-content:space-between;font-size:.82rem;
          color:#888;padding:4px 0;border-bottom:1px solid #f4f4f4}
.info-row:last-child{border-bottom:none}
.info-val{font-weight:600;color:#333;font-family:monospace}
.empty{color:#aaa;font-size:.85rem;text-align:center;padding:10px 0}
.spinning{display:inline-block;animation:spin 1s linear infinite}
@keyframes spin{to{transform:rotate(360deg)}}
</style>
</head>
<body>
<h1>&#x1F4F6; Wi-Fi &#x8A2D;&#x5B9A;
  {badge}
</h1>
<p class="sub">Web UI&#xFF1A;<b>http://{web_url}/</b></p>

<div class="card">
  <h2>&#x5DF2;&#x8A2D;&#x5B9A;&#x7684;&#x7DB2;&#x8DEF; ({net_count})</h2>
  {net_rows}
</div>

<div class="card">
  <h2>
    <span>&#x9644;&#x8FD1;&#x7684; Wi-Fi</span>
    <button class="btn scan-btn" onclick="doScan()" id="scan-btn">&#x1F50D; &#x6383;&#x63CF;</button>
  </h2>
  <div id="scan-results">
    <p class="scan-placeholder">&#x9EDE;&#x64CA;&#x300C;&#x6383;&#x63CF;&#x300D;&#x641C;&#x5C0B;&#x9644;&#x8FD1;&#x7DB2;&#x8DEF;&#xFF0C;&#x9EDE;&#x9078;&#x5F8C;&#x81EA;&#x52D5;&#x586B;&#x5165;&#x540D;&#x7A31;</p>
  </div>
</div>

<div class="card" id="add-form">
  <h2>&#x65B0;&#x589E; / &#x66F4;&#x65B0; Wi-Fi</h2>
  <form method="POST" action="/add">
    <input type="text" id="ssid-input" name="ssid"
           placeholder="Wi-Fi &#x540D;&#x7A31; (SSID)" required autocomplete="off">
    <input type="password" id="pass-input" name="password"
           placeholder="&#x5BC6;&#x78BC;&#xFF08;&#x958B;&#x653E;&#x7DB2;&#x8DEF;&#x8ACB;&#x7559;&#x7A7A;&#xFF09;" autocomplete="new-password">
    <button class="add" type="submit">&#x2713; &#x5132;&#x5B58;&#x4E26;&#x5957;&#x7528;</button>
  </form>
</div>

<div class="card">
  <h2>AP &#x8CC7;&#x8A0A; &amp; &#x63A7;&#x5236;</h2>
  <div class="info-row"><span>SSID</span><span class="info-val">{ap_ssid}</span></div>
  <div class="info-row"><span>&#x5BC6;&#x78BC;</span><span class="info-val">{ap_pass}</span></div>
  <div class="info-row"><span>Web UI</span><span class="info-val">http://{web_url}/</span></div>
  {auto_row}
  <br>
  <form method="POST" action="/stop">
    {stop_btn}
  </form>
</div>

<script>
function sigBar(s){{
  if(s>=75)return'\u25AE\u25AE\u25AE\u25AE';
  if(s>=50)return'\u25AE\u25AE\u25AE\u25AF';
  if(s>=25)return'\u25AE\u25AE\u25AF\u25AF';
  return'\u25AE\u25AF\u25AF\u25AF';
}}
function esc(s){{
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}}
function selectSSID(ssid){{
  document.getElementById('ssid-input').value=ssid;
  document.getElementById('pass-input').focus();
  document.getElementById('add-form').scrollIntoView({{behavior:'smooth'}});
}}
function doScan(){{
  const btn=document.getElementById('scan-btn');
  const box=document.getElementById('scan-results');
  btn.disabled=true;
  btn.innerHTML='<span class="spinning">\u27F3</span> \u6383\u63CF\u4E2D...';
  box.innerHTML='<p class="scan-placeholder">\u6383\u63CF\u4E2D\uFF0C\u8ACB\u7A0D\u5019\uFF0820\uFF5E30 \u79D2\uFF09...</p>';
  fetch('/scan')
    .then(r=>{{if(!r.ok)throw new Error(r.status);return r.json();}})
    .then(nets=>{{
      btn.disabled=false;
      btn.innerHTML='\uD83D\uDD0D \u6383\u63CF';
      if(!nets||nets.length===0){{
        box.innerHTML='<p class="scan-placeholder">\u672A\u627E\u5230\u4EFB\u4F55\u7DB2\u8DEF</p>';
        return;
      }}
      box.innerHTML=nets.map(n=>
        '<div class="scan-item" onclick="selectSSID(\''+esc(n.ssid)+'\')">'+
        '<span class="scan-ssid">'+esc(n.ssid)+(n.secure?' \uD83D\uDD12':'')+' </span>'+
        '<span class="scan-sig">'+sigBar(n.signal)+' '+n.signal+'%</span>'+
        '</div>'
      ).join('');
    }})
    .catch(()=>{{
      btn.disabled=false;
      btn.innerHTML='\uD83D\uDD0D \u6383\u63CF';
      box.innerHTML='<p class="scan-placeholder">\u6383\u63CF\u5931\u6557\uFF0C\u8ACB\u624B\u52D5\u8F38\u5165 SSID</p>';
    }});
}}
</script>
</body>
</html>"""


def _esc(s: str) -> str:
    """HTML 跳脫"""
    return (s.replace("&", "&amp;").replace("<", "&lt;")
             .replace(">", "&gt;").replace('"', "&quot;"))


def render_index(ap: dict) -> str:
    nets = load_from_nm()

    # 已設定網路列表
    if nets:
        rows = []
        for n in nets:
            sub = "密碼已設定" if n["password"] else "無密碼 / 未知"
            rows.append(
                f'<div class="net">'
                f'<div><div class="net-name">{_esc(n["ssid"])}</div>'
                f'<div class="net-sub">{sub}</div></div>'
                f'<form method="POST" action="/delete" style="margin:0">'
                f'<input type="hidden" name="ssid" value="{_esc(n["ssid"])}">'
                f'<button class="btn del" type="submit">&#x522A;&#x9664;</button>'
                f'</form></div>'
            )
        net_rows = "\n".join(rows)
    else:
        net_rows = '<p class="empty">&#x5C1A;&#x672A;&#x8A2D;&#x5B9A;&#x4EFB;&#x4F55; Wi-Fi &#x7DB2;&#x8DEF;</p>'

    badge = (
        '<span class="badge badge-auto">&#x81EA;&#x52D5;&#x6A21;&#x5F0F;</span>'
        if _is_auto else
        '<span class="badge badge-manual">&#x624B;&#x52D5;&#x6A21;&#x5F0F;</span>'
    )

    auto_row = (
        '<div class="info-row"><span>&#x6A21;&#x5F0F;</span>'
        '<span class="info-val">&#x81EA;&#x52D5;&#xFF08;WiFi &#x65B7;&#x7DDA;&#x81EA;&#x52D5;&#x555F;&#x52D5;&#xFF09;</span></div>'
        if _is_auto else ""
    )

    stop_btn = (
        '<button class="stop" type="submit">&#x23F9; &#x95DC;&#x9589; AP&#xFF08;&#x7E7C;&#x7E8C;&#x76E3;&#x63A7; WiFi&#xFF09;</button>'
        if _is_auto else
        '<button class="stop" type="submit">&#x23F9; &#x95DC;&#x9589; AP&#xFF0C;&#x5207;&#x63DB;&#x56DE; WiFi</button>'
    )

    return HTML_TEMPLATE.format(
        badge=badge,
        web_url=AP_IP,
        net_count=len(nets),
        net_rows=net_rows,
        ap_ssid=_esc(ap.get("ssid", "")),
        ap_pass=_esc(ap.get("password", "")),
        auto_row=auto_row,
        stop_btn=stop_btn,
    )

# ─── HTTP 處理器 ──────────────────────────────────────────────────────────────

PAGE_STOP_AUTO = """\
<!DOCTYPE html><html lang="zh-TW">
<head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>AP &#x5DF2;&#x95DC;&#x9589;</title>
<style>body{font-family:sans-serif;text-align:center;padding:48px 16px;background:#f0f2f5}
h2{font-size:1.3rem;margin-bottom:12px}p{color:#666;line-height:1.8}</style></head>
<body><h2>&#x2705; AP &#x5DF2;&#x95DC;&#x9589;</h2>
<p>&#x81EA;&#x52D5;&#x6A21;&#x5F0F;&#x6301;&#x7E8C;&#x76E3;&#x63A7;&#x4E2D;&#x3002;<br>
WiFi &#x65B7;&#x7DDA;&#x6642;&#x5C07;&#x81EA;&#x52D5;&#x91CD;&#x65B0;&#x555F;&#x52D5; AP&#x3002;</p>
</body></html>"""

PAGE_STOP_MANUAL = """\
<!DOCTYPE html><html lang="zh-TW">
<head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>AP &#x5DF2;&#x95DC;&#x9589;</title>
<style>body{font-family:sans-serif;text-align:center;padding:48px 16px;background:#f0f2f5}
h2{font-size:1.3rem;margin-bottom:12px}p{color:#666;line-height:1.8}</style></head>
<body><h2>&#x2705; AP &#x5DF2;&#x95DC;&#x9589;</h2>
<p>&#x88DD;&#x7F6E;&#x6B63;&#x5728;&#x5207;&#x63DB;&#x56DE; WiFi&#xFF0C;<br>
&#x8ACB;&#x91CD;&#x65B0;&#x9023;&#x7DDA;&#x5F8C;&#x7E7C;&#x7E8C;&#x4F7F;&#x7528;&#x3002;</p>
</body></html>"""


class Handler(BaseHTTPRequestHandler):

    def log_message(self, fmt, *args):
        pass  # 靜默 access log

    def _send(self, code: int, ct: str, body: str | bytes) -> None:
        if isinstance(body, str):
            body = body.encode()
        self.send_response(code)
        self.send_header("Content-Type", ct)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _redirect(self, path: str = "/") -> None:
        self.send_response(303)
        self.send_header("Location", path)
        self.end_headers()

    def _parse_body(self) -> dict[str, str]:
        length = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(length).decode(errors='replace')
        result = {}
        for pair in raw.split("&"):
            if "=" in pair:
                k, _, v = pair.partition("=")
                result[unquote_plus(k)] = unquote_plus(v)
        return result

    def do_GET(self):
        path = urlparse(self.path).path
        if path == "/":
            with _ap_lock:
                ap = dict(_ap_status)
            self._send(200, "text/html; charset=utf-8", render_index(ap))
        elif path == "/scan":
            results = scan_wifi()
            body = json.dumps(results, ensure_ascii=False)
            self._send(200, "application/json", body)
        else:
            self._send(404, "text/plain", "Not Found")

    def do_POST(self):
        path = urlparse(self.path).path
        form = self._parse_body()

        if path == "/add":
            ssid = form.get("ssid", "").strip()
            password = form.get("password", "")
            if ssid:
                apply_to_nm(ssid, password)
            self._redirect("/")

        elif path == "/delete":
            ssid = form.get("ssid", "")
            if ssid:
                delete_from_nm(ssid)
            self._redirect("/")

        elif path == "/stop":
            stop_ap()
            clear_status()
            if _is_auto:
                self._send(200, "text/html; charset=utf-8", PAGE_STOP_AUTO)
            else:
                self._send(200, "text/html; charset=utf-8", PAGE_STOP_MANUAL)
                threading.Timer(1.0, lambda: os._exit(0)).start()
        else:
            self._redirect("/")

# ─── HTTP 伺服器 ──────────────────────────────────────────────────────────────

def start_http_server(port: int) -> None:
    server = HTTPServer(("", port), Handler)
    logging.info("HTTP 服務啟動: :%d", port)
    server.serve_forever()

# ─── 獨立模式（立即建立 AP）──────────────────────────────────────────────────

def run_standalone_mode(ssid: str, port: int, password: str) -> None:
    if not password:
        password = gen_password()
    logging.info("正在啟動 AP: SSID=%s  Password=%s", ssid, password)
    try:
        start_ap(ssid, password)
    except Exception as e:
        logging.error("AP 啟動失敗: %s", e)
        sys.exit(1)
    set_status(True, ssid, password)
    logging.info("✅ AP 已啟動  Web UI: http://%s/", AP_IP)

    def _sig(sig_num, frame):
        logging.info("收到結束訊號，清理中...")
        stop_ap()
        clear_status()
        sys.exit(0)

    signal.signal(signal.SIGTERM, _sig)
    signal.signal(signal.SIGINT,  _sig)
    start_http_server(port)

# ─── 自動模式（定時偵測，斷線自動建立 AP）────────────────────────────────────

def run_auto_mode(ssid: str, port: int, password: str, interval: int) -> None:
    if not password:
        password = get_or_create_password()
    logging.info("自動模式啟動 | SSID=%s | 密碼=%s | 偵測間隔=%ds", ssid, password, interval)
    logging.info("Web UI（AP 啟動後）: http://%s/", AP_IP)

    def _sig(sig_num, frame):
        logging.info("收到結束訊號，清理中...")
        stop_ap()
        clear_status()
        sys.exit(0)

    signal.signal(signal.SIGTERM, _sig)
    signal.signal(signal.SIGINT,  _sig)

    # HTTP server 持續在背景執行
    threading.Thread(target=start_http_server, args=(port,), daemon=True).start()

    ap_running       = False
    disconnect_count = 0
    START_THRESHOLD  = 2  # 連續 N 次未連線才啟動 AP

    while True:
        connected = is_wifi_connected()

        if not connected and not ap_running:
            disconnect_count += 1
            logging.info("WiFi 未連線（%d/%d）", disconnect_count, START_THRESHOLD)
            if disconnect_count >= START_THRESHOLD:
                logging.info("▶  WiFi 未連線，啟動 AP 模式...")
                try:
                    start_ap(ssid, password)
                    ap_running = True
                    set_status(True, ssid, password)
                    logging.info("✅ AP 已啟動 SSID=%s PW=%s | Web: http://%s/",
                                 ssid, password, AP_IP)
                except Exception as e:
                    logging.error("AP 啟動失敗: %s", e)
                    disconnect_count = 0  # 失敗後重試

        elif connected and ap_running:
            logging.info("✅ WiFi 已連線，關閉 AP 模式")
            stop_ap()
            ap_running = False
            clear_status()
            disconnect_count = 0

        elif connected and not ap_running:
            disconnect_count = 0

        # else: AP 運行中，等待使用者設定 WiFi

        time.sleep(interval)

# ─── 主程式 ──────────────────────────────────────────────────────────────────

def main() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
    )

    parser = argparse.ArgumentParser(description="無頭樹莓派 WiFi 設定工具")
    parser.add_argument("-ssid",     default="PiZero-Setup", help="AP 熱點名稱")
    parser.add_argument("-port",     default="80",           help="HTTP 服務埠號")
    parser.add_argument("-password", default="",             help="AP 密碼（留空自動產生）")
    parser.add_argument("-auto",     action="store_true",    help="自動模式：定時偵測 WiFi，斷線時自動啟動 AP")
    parser.add_argument("-interval", default="30s",          help="自動模式偵測間隔（如 30s、1m）")
    args = parser.parse_args()

    # 解析間隔字串（支援 30s、1m 格式）
    interval_str = args.interval
    if interval_str.endswith("m"):
        interval_sec = int(interval_str[:-1]) * 60
    elif interval_str.endswith("s"):
        interval_sec = int(interval_str[:-1])
    else:
        interval_sec = int(interval_str)

    global _is_auto
    _is_auto = args.auto

    os.makedirs(CONFIG_DIR, mode=0o700, exist_ok=True)

    if args.auto:
        run_auto_mode(args.ssid, int(args.port), args.password, interval_sec)
    else:
        run_standalone_mode(args.ssid, int(args.port), args.password)


if __name__ == "__main__":
    main()
