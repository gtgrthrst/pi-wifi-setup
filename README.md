# pi-wifi-setup

Headless Raspberry Pi WiFi configuration tool via AP mode.

Triggered by long-pressing the button on the **IP page** of [oled-monitor](https://github.com/gtgrthrst/water-level-monitor), or run standalone.

## Features

- Creates a WiFi hotspot (AP) using NetworkManager (`nmcli`)
- Serves a mobile-friendly web UI at `http://10.42.0.1/`
- Shows all existing WiFi networks already configured on the Pi
- Scans nearby SSIDs and lets you click to select
- Adds / updates / deletes WiFi networks (applied to NetworkManager)
- Writes `/run/wifi-ap.json` so OLED can display AP name & password
- Graceful shutdown: stops AP, Pi auto-reconnects to saved WiFi

## Requirements

- Raspberry Pi with `wlan0`
- NetworkManager (`nmcli` must be available)
- Run as **root** (needed for hotspot and reading NM secrets)

## Build

```bash
GOARCH=arm64 GOOS=linux go build -o wifi-setup .
```

## Install

```bash
sudo cp wifi-setup /home/pi/wifi-setup/wifi-setup
sudo cp wifi-setup.service /etc/systemd/system/
sudo systemctl daemon-reload
```

The service is started **on demand** (not enabled at boot):

```bash
sudo systemctl start wifi-setup   # start AP
sudo systemctl stop  wifi-setup   # stop AP
```

## Usage

1. Start the service (or long-press IP page on oled-monitor)
2. Connect your phone/laptop to **PiZero-Setup** (password shown on OLED or in logs)
3. Open `http://10.42.0.1/` in a browser
4. Click **掃描** to find nearby networks, tap a result to fill the SSID
5. Enter the password and click **儲存並套用**
6. Click **關閉 AP** — the Pi reconnects to the configured WiFi

## Configuration

| Flag | Default | Description |
|------|---------|-------------|
| `-ssid` | `PiZero-Setup` | AP hotspot name |
| `-port` | `80` | HTTP server port |

AP password is randomly generated on each start (8 chars, no ambiguous characters).

## Status file

`/run/wifi-ap.json` is written while the AP is active:

```json
{
  "active": true,
  "ssid": "PiZero-Setup",
  "password": "abc12345",
  "webURL": "10.42.0.1"
}
```

oled-monitor reads this file to display AP info on the IP page.
