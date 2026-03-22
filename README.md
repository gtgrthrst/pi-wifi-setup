# pi-wifi-setup

**無頭樹莓派 WiFi 設定工具**（透過 AP 熱點模式）

Headless Raspberry Pi WiFi configuration tool via AP mode.

---

## 簡介

在沒有螢幕、鍵盤的樹莓派上，透過臨時建立 WiFi 熱點（AP 模式），讓手機或電腦連線後透過網頁介面管理 WiFi 設定。

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

---

## 需求

| 項目 | 說明 |
|------|------|
| 硬體 | 樹莓派（含 `wlan0` 無線網卡） |
| OS | Raspberry Pi OS Bookworm（NetworkManager 預設啟用） |
| 工具 | `nmcli`（NetworkManager CLI） |
| 權限 | 需以 **root** 執行（建立熱點及讀取 NM 密碼） |

---

## 編譯

```bash
# 在本機交叉編譯（針對 arm64）
GOARCH=arm64 GOOS=linux go build -o wifi-setup .

# 或直接在樹莓派上編譯
go build -o wifi-setup .
```

---

## 安裝

```bash
# 上傳二進位檔到樹莓派
scp wifi-setup pi@<Pi_IP>:/home/pi/wifi-setup/wifi-setup

# 安裝 systemd 服務
sudo cp wifi-setup.service /etc/systemd/system/
sudo systemctl daemon-reload
```

服務設定為**按需啟動**（不隨開機自動啟動），由 oled-monitor 或手動控制：

```bash
sudo systemctl start wifi-setup   # 啟動 AP
sudo systemctl stop  wifi-setup   # 關閉 AP
```

---

## 使用方式

1. 啟動服務（或在 oled-monitor IP 頁面長按按鈕 ≥3 秒）
2. 用手機 / 電腦連線到熱點 **`PiZero-Setup`**
   - 密碼顯示於 OLED 螢幕，或查看 systemd 日誌：
     ```bash
     sudo journalctl -u wifi-setup -n 5
     ```
3. 開啟瀏覽器，前往 **`http://10.42.0.1/`**
4. 點擊 **🔍 掃描** 搜尋附近網路，點選目標 SSID 自動填入
5. 輸入密碼，點擊 **✓ 儲存並套用**
6. 點擊 **⏹ 關閉 AP** — 樹莓派重新連線至已設定的 WiFi

---

## 啟動參數

| 參數 | 預設值 | 說明 |
|------|--------|------|
| `-ssid` | `PiZero-Setup` | AP 熱點名稱 |
| `-port` | `80` | HTTP 服務埠號 |

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

## 授權

MIT License
