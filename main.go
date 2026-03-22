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
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ─── 常數 ─────────────────────────────────────────────────────────────────────

const (
	apConName  = "wifi-setup-ap"
	apIP       = "10.42.0.1"
	statusFile = "/run/wifi-ap.json"
	configFile = "/etc/wifi-setup/networks.json"
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
	Password string `json:"password"` // 留空表示未知
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
)

// ─── nmcli 解析輔助 ────────────────────────────────────────────────────────────

// splitTerse 正確解析 nmcli -t 輸出（處理 SSID 內含冒號的情況 `\:`）
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
	out, err := exec.Command("nmcli", "-g", field, "connection", "show", conName).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ─── 網路管理 ─────────────────────────────────────────────────────────────────

// loadFromNM 從 NetworkManager 讀取所有 WiFi 連線（排除 AP 自身）
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
		if len(parts) < 2 {
			continue
		}
		name, typ := parts[0], parts[1]
		if typ != "802-11-wireless" || name == apConName {
			continue
		}
		ssid := nmGet("802-11-wireless.ssid", name)
		if ssid == "" {
			ssid = name
		}
		// 嘗試取得密碼（需 root）
		pass, _ := exec.Command("nmcli", "-s", "-g",
			"802-11-wireless-security.psk", "connection", "show", name).Output()
		nets = append(nets, Network{SSID: ssid, Password: strings.TrimSpace(string(pass))})
	}
	return nets
}

// applyToNM 將一組帳密寫入 NetworkManager
func applyToNM(n Network) {
	out, err := exec.Command("nmcli", "-t", "-f", "NAME,TYPE", "connection", "show").Output()
	conName := n.SSID
	exists := false
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			parts := splitTerse(strings.TrimSpace(line))
			if len(parts) >= 2 && parts[0] == n.SSID && parts[1] == "802-11-wireless" {
				exists = true
				break
			}
		}
	}
	if exists {
		args := []string{"connection", "modify", conName}
		if n.Password != "" {
			args = append(args, "wifi-sec.psk", n.Password)
		}
		exec.Command("nmcli", args...).Run()
	} else {
		args := []string{
			"connection", "add",
			"type", "wifi",
			"ifname", "wlan0",
			"con-name", n.SSID,
			"ssid", n.SSID,
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

	out, err := exec.CommandContext(ctx, "nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY",
		"device", "wifi", "list", "--rescan", "yes").Output()
	if err != nil {
		// fallback：用快取結果（不強制重新掃描）
		out, err = exec.CommandContext(ctx, "nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY",
			"device", "wifi", "list").Output()
		if err != nil {
			return nil, err
		}
	}

	seen := map[string]bool{}
	var results []ScanResult
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := splitTerse(line)
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
	sort.Slice(results, func(i, j int) bool {
		return results[i].Signal > results[j].Signal
	})
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

func startAP(ssid, pass string) error {
	exec.Command("nmcli", "connection", "delete", apConName).Run()
	out, err := exec.Command("nmcli", "device", "wifi", "hotspot",
		"ifname", "wlan0",
		"con-name", apConName,
		"ssid", ssid,
		"password", pass,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nmcli hotspot 失敗: %w\n%s", err, string(out))
	}
	return nil
}

func stopAP() {
	exec.Command("nmcli", "connection", "delete", apConName).Run()
}

func writeStatus() {
	mu.RLock()
	s := apStatus
	mu.RUnlock()
	data, _ := json.Marshal(s)
	_ = os.WriteFile(statusFile, data, 0644)
}

func clearStatus() {
	_ = os.WriteFile(statusFile, []byte(`{"active":false}`), 0644)
}

// ─── HTML 模板 ────────────────────────────────────────────────────────────────

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
.row{display:flex;gap:8px}
.btn{padding:9px 16px;border-radius:8px;border:none;cursor:pointer;
     font-size:.88rem;font-weight:500;white-space:nowrap}
.del{background:#fff0f0;color:#e53e3e}
.scan-btn{background:#f0f4ff;color:#1a73e8;flex:none}
.add{background:#1a73e8;color:#fff;width:100%;padding:12px;
     font-size:.95rem;border-radius:8px;border:none;cursor:pointer}
.stop{background:#ff9500;color:#fff;width:100%;padding:12px;
      font-size:.95rem;border-radius:8px;border:none;cursor:pointer}
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
<h1>📶 Wi-Fi 設定</h1>
<p class="sub">已連線 AP → 前往 <b>http://{{.WebURL}}/</b> 管理 Wi-Fi</p>

{{/* ── 已儲存網路 ── */}}
<div class="card">
  <h2>已設定的網路 ({{len .Networks}})</h2>
  {{if .Networks}}
    {{range .Networks}}
    <div class="net">
      <div>
        <div class="net-name">{{.SSID}}</div>
        <div class="net-sub">{{if .Password}}密碼已設定{{else}}無密碼 / 未知{{end}}</div>
      </div>
      <form method="POST" action="/delete" style="margin:0">
        <input type="hidden" name="ssid" value="{{.SSID}}">
        <button class="btn del" type="submit">刪除</button>
      </form>
    </div>
    {{end}}
  {{else}}
    <p class="empty">尚未設定任何 Wi-Fi 網路</p>
  {{end}}
</div>

{{/* ── 掃描附近 ── */}}
<div class="card">
  <h2>
    <span>附近的 Wi-Fi</span>
    <button class="btn scan-btn" onclick="doScan()" id="scan-btn">🔍 掃描</button>
  </h2>
  <div id="scan-results">
    <p class="scan-placeholder">點擊「掃描」搜尋附近網路，點選後自動填入名稱</p>
  </div>
</div>

{{/* ── 新增 / 更新 ── */}}
<div class="card" id="add-form">
  <h2>新增 / 更新 Wi-Fi</h2>
  <form method="POST" action="/add">
    <input type="text" id="ssid-input" name="ssid"
           placeholder="Wi-Fi 名稱 (SSID)" required autocomplete="off">
    <input type="password" id="pass-input" name="password"
           placeholder="密碼（開放網路請留空）" autocomplete="new-password">
    <button class="add" type="submit">✓ 儲存並套用</button>
  </form>
</div>

{{/* ── AP 資訊 + 關閉 ── */}}
<div class="card">
  <h2>AP 資訊 &amp; 控制</h2>
  <div class="info-row"><span>SSID</span><span class="info-val">{{.SSID}}</span></div>
  <div class="info-row"><span>密碼</span><span class="info-val">{{.Password}}</span></div>
  <div class="info-row"><span>Web UI</span><span class="info-val">http://{{.WebURL}}/</span></div>
  <br>
  <form method="POST" action="/stop">
    <button class="stop" type="submit">⏹ 關閉 AP，切換回 Wi-Fi</button>
  </form>
</div>

<script>
function sigBar(s){
  if(s>=75)return'▮▮▮▮';
  if(s>=50)return'▮▮▮░';
  if(s>=25)return'▮▮░░';
  return'▮░░░';
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
  btn.innerHTML='<span class="spinning">⟳</span> 掃描中...';
  box.innerHTML='<p class="scan-placeholder">掃描中，請稍候（約 10～20 秒）...</p>';
  fetch('/scan')
    .then(r=>{if(!r.ok)throw new Error(r.status);return r.json();})
    .then(nets=>{
      btn.disabled=false;
      btn.textContent='🔍 掃描';
      if(!nets||nets.length===0){
        box.innerHTML='<p class="scan-placeholder">未找到任何網路</p>';
        return;
      }
      box.innerHTML=nets.map(n=>
        '<div class="scan-item" onclick="selectSSID(\''+esc(n.ssid)+'\')">'+
        '<span class="scan-ssid">'+esc(n.ssid)+(n.secure?' \uD83D\uDD12':'')+' </span>'+
        '<span class="scan-sig">'+sigBar(n.signal)+' '+n.signal+'%</span>'+
        '</div>'
      ).join('');
    })
    .catch(e=>{
      btn.disabled=false;
      btn.textContent='🔍 掃描';
      box.innerHTML='<p class="scan-placeholder">掃描失敗，請手動輸入 SSID</p>';
    });
}
</script>
</body>
</html>`))

// ─── HTTP 處理器 ──────────────────────────────────────────────────────────────

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
		"WebURL":   ap.WebURL,
	})
}

func handleScan(w http.ResponseWriter, r *http.Request) {
	results, err := scanWifi()
	if err != nil {
		log.Printf("掃描失敗: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
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
	ssid := r.FormValue("ssid")
	if ssid != "" {
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
	mu.Lock()
	apStatus.Active = false
	mu.Unlock()
	clearStatus()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html><html lang="zh-TW">
<head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>AP 已關閉</title>
<style>body{font-family:sans-serif;text-align:center;padding:48px 16px;background:#f0f2f5}
h2{font-size:1.4rem;margin-bottom:12px}p{color:#666;line-height:1.6}</style></head>
<body><h2>✅ AP 已關閉</h2>
<p>裝置正在切換回 Wi-Fi，<br>請重新連線後繼續使用。</p></body></html>`)

	go func() {
		time.Sleep(time.Second)
		os.Exit(0)
	}()
}

// ─── 主程式 ──────────────────────────────────────────────────────────────────

func main() {
	ssidFlag := flag.String("ssid", "PiZero-Setup", "AP SSID")
	portFlag := flag.String("port", "80", "HTTP port")
	flag.Parse()

	pass := genPassword()
	log.Printf("正在啟動 AP: SSID=%s  Password=%s", *ssidFlag, pass)

	if err := startAP(*ssidFlag, pass); err != nil {
		log.Fatalf("AP 啟動失敗: %v", err)
	}

	mu.Lock()
	apStatus = APStatus{
		Active:   true,
		SSID:     *ssidFlag,
		Password: pass,
		WebURL:   apIP,
	}
	mu.Unlock()
	writeStatus()
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

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/scan", handleScan)
	http.HandleFunc("/add", handleAdd)
	http.HandleFunc("/delete", handleDelete)
	http.HandleFunc("/stop", handleStop)

	log.Printf("HTTP 服務啟動: :%s", *portFlag)
	log.Fatal(http.ListenAndServe(":"+*portFlag, nil))
}
