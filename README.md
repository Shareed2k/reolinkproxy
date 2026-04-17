# Reolink Proxy

A lightweight, high-performance proxy written in Go that translates Reolink's proprietary "Baichuan" protocol into standard RTSP streams and a fully compliant ONVIF API.

Perfect for integrating Reolink battery-powered cameras (which often lack native RTSP/ONVIF), or cameras located behind restrictive firewalls/NATs (using Reolink's UID P2P mechanism), into NVRs and smart home platforms like **Frigate**, **Home Assistant**, and **Synology Surveillance Station**.

## Features

* **Proprietary Protocol Support**: Connects to cameras via Local IP (TCP) or Reolink UID (UDP/P2P).
* **Standard RTSP Server**: Repackages raw H.264/H.265 video directly from the camera without transcoding video.
* **Audio Transcoding**: Automatically transcodes proprietary Reolink ADPCM audio into standard G.711 A-law (PCMA) on-the-fly for maximum VMS compatibility, while passing through AAC unmodified.
* **Full ONVIF Support**: 
  * Implements `Device` and `Media` ONVIF services.
  * WS-Security (UsernameToken) Authentication.
  * Properly maps multiple profiles to a single `VideoSource` to satisfy strict clients.
* **WS-Discovery**: Broadcasts standard ONVIF multicast packets so your camera is automatically discovered on your local network.
* **Multi-Stream Multiplexing**: Pull both the `main` and `sub` streams simultaneously over a single connection and expose them as separate ONVIF profiles.
* **Auto-Recovery Watchdog**: Monitors video frames and automatically restarts the connection if the camera silently stalls or drops the P2P connection.
* **MQTT & Home Assistant Auto-Discovery**: Natively publishes camera motion events (AI/PIR) to MQTT and enables remote controls (Reboot, PTZ, Siren, Battery Query).

## Getting Started

The easiest way to run the proxy is via Docker Compose.

### Docker Compose

Create a `docker-compose.yml` file:

```yaml
services:
  reolinkproxy:
    image: ghcr.io/shareed2k/reolinkproxy:latest # (or build locally: build: .)
    container_name: reolinkproxy
    restart: unless-stopped
    # Highly recommended for ONVIF WS-Discovery (multicast) auto-discovery to work properly
    network_mode: host
    environment:
      # --- Camera Connection ---
      # Connect via IP:
      - REOLINK_HOST=192.168.1.100
      # OR connect via UID (P2P):
      # - REOLINK_UID=95270007FFRVTAS7
      
      - REOLINK_USERNAME=admin
      - REOLINK_PASSWORD=your_camera_password
      
      # Comma-separated list of streams to pull (main, sub, extern)
      - REOLINK_STREAM=main,sub
      - REOLINK_CHANNEL=0
      
      # --- ONVIF & Proxy Configuration ---
      # Secure your local ONVIF endpoint (if omitted, ONVIF is unauthenticated)
      - ONVIF_USERNAME=admin
      - ONVIF_PASSWORD=secret_onvif_password
      
      # Optional metadata overrides
      - DEVICE_NAME=Front Door Camera
      - DEVICE_MODEL=Argus 3 Ultra
      
      # --- MQTT Integration (Optional) ---
      # Enables real-time Motion/AI events and remote PTZ/Siren control 
      - MQTT_BROKER=tcp://192.168.1.100:1883
      - MQTT_USERNAME=your_mqtt_user
      - MQTT_PASSWORD=your_mqtt_pass
      - MQTT_TOPIC=reolinkproxy
```

Start the container:
```bash
docker-compose up -d
```

## Configuration Reference

You can configure the proxy using command-line flags or environment variables. Environment variables are recommended for Docker.

| Environment Variable | CLI Flag | Default | Description |
| :--- | :--- | :--- | :--- |
| `REOLINK_HOST` | `-host` | `""` | Camera IP address. |
| `REOLINK_UID` | `-uid` | `""` | Camera UID for P2P UDP connection. (Use *either* HOST or UID) |
| `REOLINK_PORT` | `-port` | `9000` | Baichuan TCP port. |
| `REOLINK_USERNAME` | `-username` | `""` | Camera username. |
| `REOLINK_PASSWORD` | `-password` | `""` | Camera password. |
| `REOLINK_STREAM` | `-stream` | `main` | Comma-separated list of streams: `main`, `sub`, `extern`. |
| `REOLINK_CHANNEL`| `-channel`| `0` | Camera channel ID. |
| `ONVIF_USERNAME` | `-onvif-username` | `admin` | Username required by VMS to access this proxy's ONVIF. |
| `ONVIF_PASSWORD` | `-onvif-password` | `""` | Password required for ONVIF. Leave blank to disable auth. |
| `ADVERTISE_HOST` | `-advertise-host` | Auto | The IP address the proxy will advertise in ONVIF XML and RTSP URLs. If running in Docker bridge mode, set this to your Docker host's IP. |
| `MQTT_BROKER`    | `-mqtt-broker`    | `""` | MQTT Broker URL (e.g. `tcp://192.168.1.100:1883`). |
| `MQTT_USERNAME`  | `-mqtt-username`  | `""` | MQTT username. |
| `MQTT_PASSWORD`  | `-mqtt-password`  | `""` | MQTT password. |
| `MQTT_TOPIC`     | `-mqtt-topic`     | `reolinkproxy` | Root topic namespace for MQTT messages. |

### Ports Used
If you are not using `network_mode: host`, you must map the following ports:
* `8554/tcp` - RTSP Server
* `8000/udp` - RTP
* `8001/udp` - RTCP
* `8002/tcp` - ONVIF API
* `3702/udp` - ONVIF WS-Discovery

## Usage with VMS / NVRs

### Frigate
In Frigate, you do not need to manually specify RTSP paths. Simply use the ONVIF integration.
Because the proxy maps both `main` and `sub` streams correctly to the ONVIF profiles, Frigate will automatically detect them:

```yaml
cameras:
  front_door:
    ffmpeg:
      inputs:
        - path: onvif://admin:secret_onvif_password@<PROXY_IP>:8002
          roles:
            - detect
            - record
```
*(Ensure you have set `ONVIF_PASSWORD` in the proxy).*

### Home Assistant
1. Go to Settings -> Devices & Services -> Add Integration.
2. Search for **ONVIF**.
3. Enter your Proxy's IP address, Port `8002`, and your `ONVIF_USERNAME`/`ONVIF_PASSWORD`.
4. It will automatically detect the Main and Sub streams and create camera entities.

### MQTT Control & Status
If you provide an `MQTT_BROKER`, the proxy will automatically connect and expose real-time topics:
* **Auto-Discovery**: Natively registers a Motion Sensor in Home Assistant.
* **Motion Status**: Publishes `on` / `off` to `reolinkproxy/<CAMERANAME>/status/motion`.
* **Battery Queries**: Send an empty payload to `reolinkproxy/<CAMERANAME>/query/battery` to instantly get `%` and JSON status.
* **Remote PTZ**: Send `left`, `right`, `up`, `down` to `reolinkproxy/<CAMERANAME>/control/ptz`.
* **Siren**: Send `on` to `reolinkproxy/<CAMERANAME>/control/siren` to instantly trigger the camera alarm.

## Building from Source

Ensure you have Go 1.25+ installed.

```bash
git clone https://github.com/shareed2k/reolinkproxy.git
cd reolinkproxy
go build -o reolinkproxy ./cmd/reolinkproxy

./reolinkproxy -host 192.168.1.100 -username admin -password secret -stream main,sub
```

## License
This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.


## Donations

If you find this code helpful please consider supporting development.

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/M4M81XYVKG)
