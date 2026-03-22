package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ─── 常數 ─────────────────────────────────────────────────────────────────────

const (
	apConName = "wifi-setup-ap"
	apIP      = "10.42.0.1"
	statusFile = "/run/wifi-ap.json"
	configDir  = "/etc/wifi-setup"
	passFile   = "/etc/wifi-setup/ap-password"
)

// ─── 資料結構 ─────────────────────────────────────────────────────────────────

type APStatus struct {
	Active   bool   `json:"active"`
	SSID     string `json:"ssid"`
	Password string `json:"password"`
	WebURL   string `json:"webURL"`
}

type Network struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
}

type ScanResult struct {
	SSID   string `json:"ssid"`
	Signal int    `json:"signal"`
	Secure bool   `json:"secure"`
}

// ─── 全域狀態 ─────────────────────────────────────────────────────────────────

var (
	mu       sync.RWMutex
	apStatus APStatus
	isAuto   bool
)

// ─── nmcli 解析輔助 ────────────────────────────────────────────────────────────

func splitTerse(line string) []string {
	var parts []string
	var cur strings.Builder
	i := 0
	for i < len(line) {
		if line[i] == '\\' && i+1 < len(line) && line[i+1] == ':' {
			cur.WriteByte(':')
			i += 2
		} else if line[i] == ':' {
			parts = append(parts, cur.String())
			cur.Reset()
			i++
		} else {
			cur.WriteByte(line[i])
			i++
		}
	}
	return append(parts, cur.String())
}

func nmGet(field, conName string) string {
	out, _ := exec.Command("nmcli", "-g", field, "connection", "show", conName).Output()
	return strings.TrimSpace(string(out))
}

// ─── WiFi 連線狀態 ────────────────────────────────────────────────────────────

// isWifiConnected 檢查 wlan0 是否已連線到非 AP 的 WiFi
func isWifiConnected() bool {
	out, err := exec.Command("nmcli", "-t", "-f",
		"DEVICE,TYPE,STATE,CONNECTION", "device").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		parts := splitTerse(strings.TrimSpace(line))
		if len(parts) < 4 {
			continue
		}
		typ, state, conn := parts[1], parts[2], parts[3]
		if typ == "wifi" && state == "connected" && conn != apConName {
			return true
		}
	}
	return false
}

// ─── 網路管理 ─────────────────────────────────────────────────────────────────

func loadFromNM() []Network {
	out, err := exec.Command("nmcli", "-t", "-f", "NAME,TYPE", "connection", "show").Output()
	if err != nil {
		return nil
	}
	var nets []Network
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := splitTerse(line)
		if len(parts) < 2 || parts[1] != "802-11-wireless" || parts[0] == apConName {
			continue
		}
		name := parts[0]
		ssid := nmGet("802-11-wireless.ssid", name)
		if ssid == "" {
			ssid = name
		}
		pass, _ := exec.Command("nmcli", "-s", "-g",
			"802-11-wireless-security.psk", "connection", "show", name).Output()
		nets = append(nets, Network{SSID: ssid, Password: strings.TrimSpace(string(pass))})
	}
	return nets
}

func applyToNM(n Network) {
	out, _ := exec.Command("nmcli", "-t", "-f", "NAME,TYPE", "connection", "show").Output()
	exists := false
	for _, line := range strings.Split(string(out), "\n") {
		parts := splitTerse(strings.TrimSpace(line))
		if len(parts) >= 2 && parts[0] == n.SSID && parts[1] == "802-11-wireless" {
			exists = true
			break
		}
	}
	if exists {
		args := []string{"connection", "modify", n.SSID}
		if n.Password != "" {
			args = append(args, "wifi-sec.psk", n.Password)
		}
		exec.Command("nmcli", args...).Run()
	} else {
		args := []string{
			"connection", "add", "type", "wifi",
			"ifname", "wlan0", "con-name", n.SSID, "ssid", n.SSID,
			"connection.autoconnect", "yes",
			"connection.autoconnect-priority", "10",
		}
		if n.Password != "" {
			args = append(args, "wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", n.Password)
		}
		exec.Command("nmcli", args...).Run()
	}
}

func deleteFromNM(ssid string) {
	exec.Command("nmcli", "connection", "delete", ssid).Run()
}

// ─── WiFi 掃描 ────────────────────────────────────────────────────────────────

func scanWifi() ([]ScanResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "nmcli", "-t", "-f",
		"SSID,SIGNAL,SECURITY", "device", "wifi", "list", "--rescan", "yes").Output()
	if err != nil {
		out, err = exec.CommandContext(ctx, "nmcli", "-t", "-f",
			"SSID,SIGNAL,SECURITY", "device", "wifi", "list").Output()
		if err != nil {
			return nil, err
		}
	}

	seen := map[string]bool{}
	var results []ScanResult
	for _, line := range strings.Split(string(out), "\n") {
		parts := splitTerse(strings.TrimSpace(line))
		if len(parts) < 2 {
			continue
		}
		ssid := strings.TrimSpace(parts[0])
		if ssid == "" || seen[ssid] {
			continue
		}
		seen[ssid] = true
		sig, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		secure := len(parts) > 2 && parts[2] != "" && parts[2] != "--"
		results = append(results, ScanResult{SSID: ssid, Signal: sig, Secure: secure})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Signal > results[j].Signal })
	return results, nil
}

// ─── AP 管理 ──────────────────────────────────────────────────────────────────

func genPassword() string {
	const chars = "abcdefghjkmnpqrstuvwxyz23456789"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[r.Intn(len(chars))]
	}
	return string(b)
}

// getOrCreatePassword 自動模式使用持久化密碼（重啟後不變）
func getOrCreatePassword() string {
	if data, err := os.ReadFile(passFile); err == nil {
		if p := strings.TrimSpace(string(data)); len(p) >= 8 {
			return p
		}
	}
	p := genPassword()
	_ = os.MkdirAll(configDir, 0700)
	_ = os.WriteFile(passFile, []byte(p), 0600)
	return p
}

func startAP(ssid, pass string) error {
	exec.Command("nmcli", "connection", "delete", apConName).Run()
	out, err := exec.Command("nmcli", "device", "wifi", "hotspot",
		"ifname", "wlan0", "con-name", apConName,
		"ssid", ssid, "password", pass,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nmcli hotspot 失敗: %w\n%s", err, string(out))
	}
	return nil
}

func stopAP() {
	exec.Command("nmcli", "connection", "delete", apConName).Run()
}

func setStatus(active bool, ssid, pass string) {
	mu.Lock()
	apStatus = APStatus{Active: active, SSID: ssid, Password: pass, WebURL: apIP}
	mu.Unlock()
	data, _ := json.Marshal(APStatus{Active: active, SSID: ssid, Password: pass, WebURL: apIP})
	_ = os.WriteFile(statusFile, data, 0644)
}

func clearStatus() {
	mu.Lock()
	apStatus = APStatus{}
	mu.Unlock()
	_ = os.WriteFile(statusFile, []byte(`{"active":false}`), 0644)
}

// ─── HTTP 處理器 ──────────────────────────────────────────────────────────────

var tmplIndex = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html lang="zh-TW">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Wi-Fi 設定</title>
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
  {{if .AutoMode}}<span class="badge badge-auto">&#x81EA;&#x52D5;&#x6A21;&#x5F0F;</span>
  {{else}}<span class="badge badge-manual">&#x624B;&#x52D5;&#x6A21;&#x5F0F;</span>{{end}}
</h1>
<p class="sub">Web UI&#xFF1A;<b>http://{{.WebURL}}/</b></p>

<div class="card">
  <h2>&#x5DF2;&#x8A2D;&#x5B9A;&#x7684;&#x7DB2;&#x8DEF; ({{len .Networks}})</h2>
  {{if .Networks}}
    {{range .Networks}}
    <div class="net">
      <div>
        <div class="net-name">{{.SSID}}</div>
        <div class="net-sub">{{if .Password}}&#x5BC6;&#x78BC;&#x5DF2;&#x8A2D;&#x5B9A;{{else}}&#x7121;&#x5BC6;&#x78BC; / &#x672A;&#x77E5;{{end}}</div>
      </div>
      <form method="POST" action="/delete" style="margin:0">
        <input type="hidden" name="ssid" value="{{.SSID}}">
        <button class="btn del" type="submit">&#x522A;&#x9664;</button>
      </form>
    </div>
    {{end}}
  {{else}}
    <p class="empty">&#x5C1A;&#x672A;&#x8A2D;&#x5B9A;&#x4EFB;&#x4F55; Wi-Fi &#x7DB2;&#x8DEF;</p>
  {{end}}
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
  <div class="info-row"><span>SSID</span><span class="info-val">{{.SSID}}</span></div>
  <div class="info-row"><span>&#x5BC6;&#x78BC;</span><span class="info-val">{{.Password}}</span></div>
  <div class="info-row"><span>Web UI</span><span class="info-val">http://{{.WebURL}}/</span></div>
  {{if .AutoMode}}
  <div class="info-row"><span>&#x6A21;&#x5F0F;</span><span class="info-val">&#x81EA;&#x52D5;&#xFF08;WiFi &#x65B7;&#x7DDA;&#x81EA;&#x52D5;&#x555F;&#x52D5;&#xFF09;</span></div>
  {{end}}
  <br>
  <form method="POST" action="/stop">
    {{if .AutoMode}}
    <button class="stop" type="submit">&#x23F9; &#x95DC;&#x9589; AP&#xFF08;&#x7E7C;&#x7E8C;&#x76E3;&#x63A7; WiFi&#xFF09;</button>
    {{else}}
    <button class="stop" type="submit">&#x23F9; &#x95DC;&#x9589; AP&#xFF0C;&#x5207;&#x63DB;&#x56DE; WiFi</button>
    {{end}}
  </form>
</div>

<script>
function sigBar(s){
  if(s>=75)return'\u25AE\u25AE\u25AE\u25AE';
  if(s>=50)return'\u25AE\u25AE\u25AE\u25AF';
  if(s>=25)return'\u25AE\u25AE\u25AF\u25AF';
  return'\u25AE\u25AF\u25AF\u25AF';
}
function esc(s){
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}
function selectSSID(ssid){
  document.getElementById('ssid-input').value=ssid;
  document.getElementById('pass-input').focus();
  document.getElementById('add-form').scrollIntoView({behavior:'smooth'});
}
function doScan(){
  const btn=document.getElementById('scan-btn');
  const box=document.getElementById('scan-results');
  btn.disabled=true;
  btn.innerHTML='<span class="spinning">\u27F3</span> \u6383\u63CF\u4E2D...';
  box.innerHTML='<p class="scan-placeholder">\u6383\u63CF\u4E2D\uFF0C\u8ACB\u7A0D\u5019\uFF0820\uFF5E30 \u79D2\uFF09...</p>';
  fetch('/scan')
    .then(r=>{if(!r.ok)throw new Error(r.status);return r.json();})
    .then(nets=>{
      btn.disabled=false;
      btn.innerHTML='\uD83D\uDD0D \u6383\u63CF';
      if(!nets||nets.length===0){
        box.innerHTML='<p class="scan-placeholder">\u672A\u627E\u5230\u4EFB\u4F55\u7DB2\u8DEF</p>';
        return;
      }
      box.innerHTML=nets.map(n=>
        '<div class="scan-item" onclick="selectSSID(\''+esc(n.ssid)+'\')">'+
        '<span class="scan-ssid">'+esc(n.ssid)+(n.secure?' \uD83D\uDD12':'')+' </span>'+
        '<span class="scan-sig">'+sigBar(n.signal)+' '+n.signal+'%</span>'+
        '</div>'
      ).join('');
    })
    .catch(()=>{
      btn.disabled=false;
      btn.innerHTML='\uD83D\uDD0D \u6383\u63CF';
      box.innerHTML='<p class="scan-placeholder">\u6383\u63CF\u5931\u6557\uFF0C\u8ACB\u624B\u52D5\u8F38\u5165 SSID</p>';
    });
}
</script>
</body>
</html>`))

func handleIndex(w http.ResponseWriter, r *http.Request) {
	nets := loadFromNM()
	mu.RLock()
	ap := apStatus
	mu.RUnlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmplIndex.Execute(w, map[string]interface{}{
		"Networks": nets,
		"SSID":     ap.SSID,
		"Password": ap.Password,
		"WebURL":   apIP,
		"AutoMode": isAuto,
	})
}

func handleScan(w http.ResponseWriter, r *http.Request) {
	results, err := scanWifi()
	if err != nil {
		log.Printf("掃描失敗: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	if results == nil {
		results = []ScanResult{}
	}
	json.NewEncoder(w).Encode(results)
}

func handleAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	ssid := strings.TrimSpace(r.FormValue("ssid"))
	pass := r.FormValue("password")
	if ssid == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	applyToNM(Network{SSID: ssid, Password: pass})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if ssid := r.FormValue("ssid"); ssid != "" {
		deleteFromNM(ssid)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	stopAP()
	clearStatus()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if isAuto {
		fmt.Fprint(w, `<!DOCTYPE html><html lang="zh-TW">
<head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>AP 已關閉</title>
<style>body{font-family:sans-serif;text-align:center;padding:48px 16px;background:#f0f2f5}
h2{font-size:1.3rem;margin-bottom:12px}p{color:#666;line-height:1.8}</style></head>
<body><h2>&#x2705; AP &#x5DF2;&#x95DC;&#x9589;</h2>
<p>&#x81EA;&#x52D5;&#x6A21;&#x5F0F;&#x6301;&#x7E8C;&#x76E3;&#x63A7;&#x4E2D;&#x3002;<br>WiFi &#x65B7;&#x7DDA;&#x6642;&#x5C07;&#x81EA;&#x52D5;&#x91CD;&#x65B0;&#x555F;&#x52D5; AP&#x3002;</p>
</body></html>`)
	} else {
		fmt.Fprint(w, `<!DOCTYPE html><html lang="zh-TW">
<head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>AP 已關閉</title>
<style>body{font-family:sans-serif;text-align:center;padding:48px 16px;background:#f0f2f5}
h2{font-size:1.3rem;margin-bottom:12px}p{color:#666;line-height:1.8}</style></head>
<body><h2>&#x2705; AP &#x5DF2;&#x95DC;&#x9589;</h2>
<p>&#x88DD;&#x7F6E;&#x6B63;&#x5728;&#x5207;&#x63DB;&#x56DE; WiFi&#xFF0C;<br>&#x8ACB;&#x91CD;&#x65B0;&#x9023;&#x7DDA;&#x5F8C;&#x7E7C;&#x7E8C;&#x4F7F;&#x7528;&#x3002;</p>
</body></html>`)
		go func() { time.Sleep(time.Second); os.Exit(0) }()
	}
}

func startHTTPServer(port string) {
	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/scan", handleScan)
	http.HandleFunc("/add", handleAdd)
	http.HandleFunc("/delete", handleDelete)
	http.HandleFunc("/stop", handleStop)
	log.Printf("HTTP 服務啟動: :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// ─── 獨立模式（立即建立 AP）────────────────────────────────────────────────────

func runStandaloneMode(ssid, port, pass string) {
	if pass == "" {
		pass = genPassword()
	}
	log.Printf("正在啟動 AP: SSID=%s  Password=%s", ssid, pass)
	if err := startAP(ssid, pass); err != nil {
		log.Fatalf("AP 啟動失敗: %v", err)
	}
	setStatus(true, ssid, pass)
	log.Printf("✅ AP 已啟動  Web UI: http://%s/", apIP)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		log.Println("收到結束訊號，清理中...")
		stopAP()
		clearStatus()
		os.Exit(0)
	}()

	startHTTPServer(port)
}

// ─── 自動模式（定時偵測，斷線自動建立 AP）─────────────────────────────────────

func runAutoMode(ssid, port, pass string, interval time.Duration) {
	if pass == "" {
		pass = getOrCreatePassword()
	}
	log.Printf("自動模式啟動 | SSID=%s | 密碼=%s | 偵測間隔=%s", ssid, pass, interval)
	log.Printf("Web UI（AP 啟動後）: http://%s/", apIP)

	go startHTTPServer(port) // HTTP server 持續運行

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		log.Println("收到結束訊號，清理中...")
		stopAP()
		clearStatus()
		os.Exit(0)
	}()

	apRunning := false
	disconnectCount := 0
	const startThreshold = 2 // 連續 N 次未連線才啟動 AP

	for {
		connected := isWifiConnected()

		switch {
		case !connected && !apRunning:
			disconnectCount++
			log.Printf("WiFi 未連線（%d/%d）", disconnectCount, startThreshold)
			if disconnectCount >= startThreshold {
				log.Println("▶  WiFi 未連線，啟動 AP 模式...")
				if err := startAP(ssid, pass); err != nil {
					log.Printf("AP 啟動失敗: %v", err)
					disconnectCount = 0 // 失敗後重試
				} else {
					apRunning = true
					setStatus(true, ssid, pass)
					log.Printf("✅ AP 已啟動 SSID=%s PW=%s | Web: http://%s/", ssid, pass, apIP)
				}
			}

		case connected && apRunning:
			log.Println("✅ WiFi 已連線，關閉 AP 模式")
			stopAP()
			apRunning = false
			clearStatus()
			disconnectCount = 0

		case connected && !apRunning:
			disconnectCount = 0

		case !connected && apRunning:
			// AP 運行中，等待使用者設定 WiFi
		}

		time.Sleep(interval)
	}
}

// ─── 主程式 ──────────────────────────────────────────────────────────────────

func main() {
	ssidFlag     := flag.String("ssid", "PiZero-Setup", "AP 熱點名稱")
	portFlag     := flag.String("port", "80", "HTTP 服務埠號")
	passwordFlag := flag.String("password", "", "AP 密碼（留空自動產生，自動模式下持久化儲存）")
	autoFlag     := flag.Bool("auto", false, "自動模式：定時偵測 WiFi，斷線時自動啟動 AP")
	intervalFlag := flag.Duration("interval", 30*time.Second, "自動模式偵測間隔（如 30s、1m）")
	flag.Parse()

	_ = os.MkdirAll(filepath.Dir(passFile), 0700)

	isAuto = *autoFlag

	if *autoFlag {
		runAutoMode(*ssidFlag, *portFlag, *passwordFlag, *intervalFlag)
	} else {
		runStandaloneMode(*ssidFlag, *portFlag, *passwordFlag)
	}
}
