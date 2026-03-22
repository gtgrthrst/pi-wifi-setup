#!/bin/bash
# install.sh — 在樹莓派上一鍵安裝 wifi-setup
# 用法：curl -fsSL https://raw.githubusercontent.com/gtgrthrst/pi-wifi-setup/main/install.sh | sudo bash
# 或：sudo bash install.sh

set -e

REPO="https://github.com/gtgrthrst/pi-wifi-setup"
INSTALL_DIR="/home/pi/wifi-setup"
SERVICE_FILE="/etc/systemd/system/wifi-setup.service"
SSID="PiZero-Setup"

# ── 確認 root ──────────────────────────────────────────────────────────────
if [ "$EUID" -ne 0 ]; then
  echo "錯誤：請以 root 執行（sudo bash install.sh）"
  exit 1
fi

echo "======================================"
echo "  pi-wifi-setup 安裝程式"
echo "======================================"

# ── 確認 NetworkManager ────────────────────────────────────────────────────
if ! command -v nmcli &>/dev/null; then
  echo "❌ 未找到 nmcli，請確認已安裝 NetworkManager"
  exit 1
fi
echo "✅ NetworkManager 已就緒"

# ── 確認 Go（若需要從原始碼編譯）──────────────────────────────────────────
BUILD_FROM_SOURCE=false
if ! [ -f "$INSTALL_DIR/wifi-setup" ]; then
  if command -v go &>/dev/null; then
    BUILD_FROM_SOURCE=true
  else
    echo ""
    echo "未找到預編譯的二進位檔，且未安裝 Go。"
    echo "請先安裝 Go 或手動複製二進位檔至 $INSTALL_DIR/wifi-setup"
    echo "安裝 Go："
    echo "  wget https://go.dev/dl/go1.24.0.linux-arm64.tar.gz"
    echo "  sudo tar -C /usr/local -xzf go1.24.0.linux-arm64.tar.gz"
    echo "  export PATH=\$PATH:/usr/local/go/bin"
    exit 1
  fi
fi

# ── 建立目錄 ───────────────────────────────────────────────────────────────
mkdir -p "$INSTALL_DIR"
echo "✅ 安裝目錄：$INSTALL_DIR"

# ── 從原始碼編譯 ───────────────────────────────────────────────────────────
if [ "$BUILD_FROM_SOURCE" = true ]; then
  echo "▶  從原始碼編譯..."
  TMP_SRC=$(mktemp -d)
  git clone --depth=1 "$REPO" "$TMP_SRC/pi-wifi-setup" 2>/dev/null || {
    # 若 git 不可用，嘗試 curl 下載
    curl -fsSL "$REPO/archive/refs/heads/main.tar.gz" | tar -xz -C "$TMP_SRC"
    mv "$TMP_SRC"/pi-wifi-setup-main "$TMP_SRC/pi-wifi-setup"
  }
  cd "$TMP_SRC/pi-wifi-setup"
  go build -o "$INSTALL_DIR/wifi-setup" .
  cd /
  rm -rf "$TMP_SRC"
  echo "✅ 編譯完成"
fi

# ── 詢問安裝模式 ───────────────────────────────────────────────────────────────
echo ""
echo "請選擇安裝模式："
echo "  1) 手動模式 — 需手動啟動 AP（適合搭配 oled-monitor）"
echo "  2) 自動模式 — 開機即監控，WiFi 斷線時自動建立 AP（適合無 OLED 裝置）"
read -rp "請輸入選項 [1/2]（直接 Enter 預設為 1）： " MODE_CHOICE
MODE_CHOICE="${MODE_CHOICE:-1}"

# ── 安裝兩種服務檔 ─────────────────────────────────────────────────────────────
cat > "/etc/systemd/system/wifi-setup.service" <<SVCEOF
[Unit]
Description=Wi-Fi 設定 AP 服務
After=NetworkManager.service
Requires=NetworkManager.service

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/wifi-setup -ssid $SSID -port 80
Restart=no
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
SVCEOF

cat > "/etc/systemd/system/wifi-autoap.service" <<SVCEOF
[Unit]
Description=Wi-Fi 自動 AP 服務（無 WiFi 時自動建立熱點）
After=NetworkManager.service network.target
Requires=NetworkManager.service

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/wifi-setup -auto -interval 30s -ssid $SSID -port 80
Restart=on-failure
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
SVCEOF

systemctl daemon-reload
echo "✅ systemd 服務已安裝（wifi-setup.service + wifi-autoap.service）"

if [ "$MODE_CHOICE" = "2" ]; then
  systemctl enable wifi-autoap.service
  systemctl start  wifi-autoap.service
  echo "✅ 自動模式已啟用並啟動（wifi-autoap.service）"
fi

# ── 完成 ───────────────────────────────────────────────────────────────────────
echo ""
echo "======================================"
echo "  安裝完成！"
echo "======================================"
echo ""
if [ "$MODE_CHOICE" = "2" ]; then
  echo "【自動模式】"
  echo "  開機自動啟動，每 30 秒偵測 WiFi 狀態"
  echo "  WiFi 斷線 → 自動建立 AP；連線後 → 自動關閉 AP"
  echo "  查看日誌：sudo journalctl -u wifi-autoap -f"
  echo "  停用：sudo systemctl disable wifi-autoap"
else
  echo "【手動模式】"
  echo "  啟動 AP：sudo systemctl start wifi-setup"
  echo "  停止 AP：sudo systemctl stop  wifi-setup"
  echo "  查看日誌：sudo journalctl -u wifi-setup -f"
fi
echo ""
echo "AP 熱點名稱：$SSID"
echo "Web UI：http://10.42.0.1/"
echo ""
