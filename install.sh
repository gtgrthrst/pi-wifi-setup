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

# ── 設定 systemd 服務 ──────────────────────────────────────────────────────
cat > "$SERVICE_FILE" <<EOF
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
EOF

systemctl daemon-reload
echo "✅ systemd 服務已安裝（wifi-setup.service）"

# ── 完成 ───────────────────────────────────────────────────────────────────
echo ""
echo "======================================"
echo "  安裝完成！"
echo "======================================"
echo ""
echo "指令："
echo "  啟動 AP：sudo systemctl start wifi-setup"
echo "  停止 AP：sudo systemctl stop  wifi-setup"
echo "  查看日誌：sudo journalctl -u wifi-setup -f"
echo ""
echo "AP 熱點名稱：$SSID"
echo "密碼：每次啟動隨機產生（請查看 OLED 或日誌）"
echo "Web UI：http://10.42.0.1/"
echo ""
