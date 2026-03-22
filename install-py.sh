#!/bin/bash
# install-py.sh — 在樹莓派上一鍵安裝 wifi-setup（Python 版）
# 用法：curl -fsSL https://raw.githubusercontent.com/gtgrthrst/pi-wifi-setup/python/install-py.sh | sudo bash
# 或：sudo bash install-py.sh

set -e

REPO="https://github.com/gtgrthrst/pi-wifi-setup"
BRANCH="python"
INSTALL_DIR="/home/pi/wifi-setup"
SCRIPT="wifi_setup.py"
SSID="PiZero-Setup"

# ── 確認 root ──────────────────────────────────────────────────────────────
if [ "$EUID" -ne 0 ]; then
  echo "錯誤：請以 root 執行（sudo bash install-py.sh）"
  exit 1
fi

echo "======================================"
echo "  pi-wifi-setup 安裝程式（Python 版）"
echo "======================================"

# ── 確認 NetworkManager ────────────────────────────────────────────────────
if ! command -v nmcli &>/dev/null; then
  echo "❌ 未找到 nmcli，請確認已安裝 NetworkManager"
  exit 1
fi
echo "✅ NetworkManager 已就緒"

# ── 確認 Python 3 ─────────────────────────────────────────────────────────
if ! command -v python3 &>/dev/null; then
  echo "❌ 未找到 python3，請先安裝：sudo apt install python3"
  exit 1
fi
echo "✅ Python 3 已就緒（$(python3 --version)）"

# ── 建立目錄並下載腳本 ────────────────────────────────────────────────────
mkdir -p "$INSTALL_DIR"
echo "✅ 安裝目錄：$INSTALL_DIR"

echo "▶  下載 $SCRIPT..."
curl -fsSL "$REPO/raw/$BRANCH/$SCRIPT" -o "$INSTALL_DIR/$SCRIPT"
chmod +x "$INSTALL_DIR/$SCRIPT"
echo "✅ 腳本已下載至 $INSTALL_DIR/$SCRIPT"

# ── 詢問安裝模式 ──────────────────────────────────────────────────────────
echo ""
echo "請選擇安裝模式："
echo "  1) 手動模式 — 需手動啟動 AP（適合搭配 oled-monitor）"
echo "  2) 自動模式 — 開機即監控，WiFi 斷線時自動建立 AP（適合無 OLED 裝置）"
read -rp "請輸入選項 [1/2]（直接 Enter 預設為 1）： " MODE_CHOICE
MODE_CHOICE="${MODE_CHOICE:-1}"

# ── 安裝兩種服務檔 ────────────────────────────────────────────────────────
cat > "/etc/systemd/system/wifi-setup-py.service" <<SVCEOF
[Unit]
Description=Wi-Fi 設定 AP 服務（Python 版）
After=NetworkManager.service
Requires=NetworkManager.service

[Service]
Type=simple
User=root
ExecStart=/usr/bin/python3 $INSTALL_DIR/$SCRIPT -ssid $SSID -port 80
Restart=no
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
SVCEOF

cat > "/etc/systemd/system/wifi-autoap-py.service" <<SVCEOF
[Unit]
Description=Wi-Fi 自動 AP 服務（Python 版，無 WiFi 時自動建立熱點）
After=NetworkManager.service network.target
Requires=NetworkManager.service

[Service]
Type=simple
User=root
ExecStart=/usr/bin/python3 $INSTALL_DIR/$SCRIPT -auto -interval 30s -ssid $SSID -port 80
Restart=on-failure
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
SVCEOF

systemctl daemon-reload
echo "✅ systemd 服務已安裝（wifi-setup-py.service + wifi-autoap-py.service）"

if [ "$MODE_CHOICE" = "2" ]; then
  systemctl enable wifi-autoap-py.service
  systemctl start  wifi-autoap-py.service
  echo "✅ 自動模式已啟用並啟動（wifi-autoap-py.service）"
fi

# ── 完成 ──────────────────────────────────────────────────────────────────
echo ""
echo "======================================"
echo "  安裝完成！"
echo "======================================"
echo ""
if [ "$MODE_CHOICE" = "2" ]; then
  echo "【自動模式】"
  echo "  開機自動啟動，每 30 秒偵測 WiFi 狀態"
  echo "  WiFi 斷線 → 自動建立 AP；連線後 → 自動關閉 AP"
  echo "  查看日誌：sudo journalctl -u wifi-autoap-py -f"
  echo "  停用：sudo systemctl disable wifi-autoap-py"
else
  echo "【手動模式】"
  echo "  啟動 AP：sudo systemctl start wifi-setup-py"
  echo "  停止 AP：sudo systemctl stop  wifi-setup-py"
  echo "  查看日誌：sudo journalctl -u wifi-setup-py -f"
fi
echo ""
echo "AP 熱點名稱：$SSID"
echo "Web UI：http://10.42.0.1/"
echo ""
