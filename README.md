# pi-wifi-setup（Python 版）

**無頭樹莓派 WiFi 設定工具**（透過 AP 熱點模式）

Headless Raspberry Pi WiFi configuration tool via AP mode — Python implementation.

> Go 版本請切換至 `main` 分支。

---

## 簡介

在沒有螢幕、鍵盤的樹莓派上，透過臨時建立 WiFi 熱點（AP 模式），讓手機或電腦連線後透過網頁介面管理 WiFi 設定。

僅需 **Python 3**（標準函式庫，無需額外安裝套件），適合資源受限或不想安裝 Go 的環境。

可搭配 [oled-monitor](https://github.com/gtgrthrst/water-level-monitor)（`zero2w` 分支），在 OLED **IP 頁面長按按鈕**即可一鍵啟動/關閉 AP。

---

## 功能

- 使用 NetworkManager（`nmcli`）建立 WiFi 熱點
- 提供行動裝置友善的網頁介面（`http://10.42.0.1/`）
- 顯示樹莓派上**已設定的所有 WiFi 連線**
- **掃描附近 WiFi**，點選結果自動填入 SSID 欄位
- 新增 / 更新 / 刪除 WiFi 帳密（直接同步至 NetworkManager）
- 將 AP 狀態寫入 `/run/wifi-ap.json`，供 oled-monitor 在 OLED 上顯示 AP 名稱與密碼
- 關閉 AP 後，樹莓派自動重新連線至已設定的 WiFi
- 收到 SIGTERM / SIGINT 時自動清理熱點連線
- **自動模式**：無 OLED 時定時偵測 WiFi，斷線自動建立 AP

---

## 需求

| 項目 | 說明 |
|------|------|
| 硬體 | 樹莓派（含 `wlan0` 無線網卡） |
| OS | Raspberry Pi OS Bookworm（NetworkManager 預設啟用） |
| Python | Python 3.9+（系統內建，無需額外安裝） |
| 工具 | `nmcli`（NetworkManager CLI） |
| 權限 | 需以 **root** 執行（建立熱點及讀取 NM 密碼） |

---

## 安裝

### 一鍵安裝（推薦）

```bash
curl -fsSL https://raw.githubusercontent.com/gtgrthrst/pi-wifi-setup/python/install-py.sh | sudo bash
```

安裝腳本會自動：
1. 確認 NetworkManager 與 Python 3 是否就緒
2. 下載 `wifi_setup.py` 至 `/home/pi/wifi-setup/`
3. 建立 systemd 服務（手動或自動模式）
4. 詢問安裝模式後啟動服務

### 手動安裝

```bash
# 下載腳本
sudo mkdir -p /home/pi/wifi-setup
sudo curl -fsSL https://raw.githubusercontent.com/gtgrthrst/pi-wifi-setup/python/wifi_setup.py \
     -o /home/pi/wifi-setup/wifi_setup.py
sudo chmod +x /home/pi/wifi-setup/wifi_setup.py

# 安裝 systemd 服務（手動模式）
sudo cp wifi-setup-py.service /etc/systemd/system/
sudo systemctl daemon-reload
```

---

## 使用方式

### 手動模式（搭配 oled-monitor）

```bash
sudo systemctl start wifi-setup-py   # 啟動 AP
sudo systemctl stop  wifi-setup-py   # 關閉 AP
```

### 自動模式（無 OLED 裝置）

```bash
sudo systemctl enable wifi-autoap-py
sudo systemctl start  wifi-autoap-py
```

開機後每 30 秒偵測 WiFi 狀態：
- WiFi 斷線 → 自動建立 AP
- WiFi 連線後 → 自動關閉 AP

---

## 啟動參數

| 參數 | 預設值 | 說明 |
|------|--------|------|
| `-ssid` | `PiZero-Setup` | AP 熱點名稱 |
| `-port` | `80` | HTTP 服務埠號 |
| `-password` | （自動產生） | AP 密碼 |
| `-auto` | false | 啟用自動模式 |
| `-interval` | `30s` | 自動模式偵測間隔（支援 `30s`、`1m`） |

```bash
# 直接執行範例
sudo python3 wifi_setup.py -ssid MyAP -port 8080
sudo python3 wifi_setup.py -auto -interval 1m -ssid MyAP
```

每次啟動時自動產生隨機 8 碼密碼（排除易混淆字元）。

---

## 狀態檔 `/run/wifi-ap.json`

AP 啟用期間寫入，供 oled-monitor 讀取：

```json
{
  "active": true,
  "ssid": "PiZero-Setup",
  "password": "abc12345",
  "webURL": "10.42.0.1"
}
```

AP 關閉後自動重設為 `{"active": false}`。

---

## 網頁介面說明

| 區塊 | 功能 |
|------|------|
| 已設定的網路 | 列出 NetworkManager 中所有 WiFi 連線，可刪除 |
| 附近的 Wi-Fi | 掃描並列出周圍 SSID（依訊號強度排序），點選填入 |
| 新增 / 更新 | 輸入 SSID 與密碼儲存，同步套用至 NetworkManager |
| AP 控制 | 顯示目前 AP 資訊，可關閉 AP |

---

## 與 oled-monitor 整合

搭配 [water-level-monitor](https://github.com/gtgrthrst/water-level-monitor)（`zero2w` 分支）的 `oled-monitor`：

| 操作 | 頁面 | 行為 |
|------|------|------|
| 短按 | 任何頁面 | 切換至下一頁 |
| 長按 ≥3s | 第 5 頁（風扇） | 切換風扇開/關 |
| 長按 ≥3s | 第 6 頁（IP） | 啟動 / 停止 wifi-setup AP |

AP 啟動後，OLED IP 頁面自動顯示：
```
[AP Mode]
PiZero-Setup
PW:abc12345
10.42.0.1
```

---

## Go 版本

功能完全相同的 Go 編譯版本請見 [`main` 分支](https://github.com/gtgrthrst/pi-wifi-setup/tree/main)，執行效率更高但需要 Go 編譯環境。

---

## 授權

MIT License
