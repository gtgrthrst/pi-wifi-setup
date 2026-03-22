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

## 重新安裝系統後如何使用

重裝系統後面臨「沒有 WiFi → 無法連線 → 無法設定 WiFi」的問題，以下三種方式擇一：

### 方法一：Raspberry Pi Imager 預設 WiFi（推薦）

刷機時在 Imager 的 ⚙️ 進階設定中填入：

```
✅ 啟用 SSH
✅ 設定 WiFi SSID / 密碼（填入目前家用 WiFi）
✅ 設定主機名稱與使用者密碼
```

開機後直接 SSH，執行一鍵安裝：

```bash
curl -fsSL https://raw.githubusercontent.com/gtgrthrst/pi-wifi-setup/main/install.sh | sudo bash
```

安裝完成後，往後 WiFi 更換時即可透過本工具修改，不需重刷。

---

### 方法二：預先複製二進位檔至 SD 卡（完全離線）

SD 卡燒錄完成後插回 Linux 電腦，掛載 rootfs（ext4）分區：

```bash
# 找分區
lsblk

# 掛載第二分區（rootfs）
sudo mount /dev/sdX2 /mnt

# 複製檔案
sudo mkdir -p /mnt/home/pi/wifi-setup
sudo cp wifi-setup /mnt/home/pi/wifi-setup/wifi-setup
sudo chmod +x /mnt/home/pi/wifi-setup/wifi-setup
sudo cp wifi-setup.service /mnt/etc/systemd/system/

# 設定開機自動啟動
sudo ln -s /etc/systemd/system/wifi-setup.service \
           /mnt/etc/systemd/system/multi-user.target.wants/wifi-setup.service

sudo umount /mnt
```

Pi 開機後自動建立 AP → 用手機連線 → 設定 WiFi → 停用 AP → 完成。

> Windows 用戶需透過 WSL2 或 Ext2Fsd 存取 ext4 分區。

---

### 方法三：透過有線網路（乙太網路）

若手邊有網路線，插上後 Pi 會自動取得 IP，直接 SSH：

```bash
# 找 Pi 的 IP（從路由器管理頁面，或用 nmap）
nmap -sn 192.168.0.0/24

ssh pi@<Pi_IP>
curl -fsSL https://raw.githubusercontent.com/gtgrthrst/pi-wifi-setup/main/install.sh | sudo bash
```

---

### 一鍵安裝腳本說明

`install.sh` 會自動：
1. 確認 NetworkManager 是否就緒
2. 若目錄中已有二進位檔則直接使用；否則從原始碼編譯
3. 建立 systemd 服務（`/etc/systemd/system/wifi-setup.service`）
4. 印出使用指令

---

## 授權

MIT License
