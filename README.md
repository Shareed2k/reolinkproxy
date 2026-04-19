# Reolink Proxy

A lightweight Go proxy that translates Reolink's proprietary Baichuan protocol into standard RTSP streams and a compliant ONVIF API.

It is aimed at battery Reolink cameras and other models that do not expose native RTSP/ONVIF, or that are easier to reach through Reolink UID/P2P than direct LAN access.

## Features

* Connects to cameras by local IP or Reolink UID.
* Repackages H.264/H.265 video to RTSP without video transcoding.
* Transcodes Reolink ADPCM audio to PCMA and passes AAC through.
* Exposes ONVIF `Device` and `Media` services with WS-Security auth support.
* Broadcasts WS-Discovery for local ONVIF discovery.
* Supports multiple streams per camera such as `main` and `sub`.
* Publishes MQTT motion and control topics for Home Assistant and similar systems.

## Configuration

The app now reads cameras from indexed environment variables:

* `REOLINK_CAMERA_0_*`
* `REOLINK_CAMERA_1_*`
* `REOLINK_CAMERA_2_*`

Each camera requires:

* `REOLINK_CAMERA_<n>_NAME`
* `REOLINK_CAMERA_<n>_HOST` or `REOLINK_CAMERA_<n>_UID`

Supported camera fields:

* `NAME`
* `HOST`
* `PORT`
* `UID`
* `USERNAME`
* `PASSWORD`
* `TIMEOUT`
* `STREAM`
* `CHANNEL`
* `RTSP_PATH`

Camera defaults:

* `PORT=9000`
* `STREAM=main`
* `TIMEOUT=10s`
* `RTSP_PATH=<NAME>/stream`

Global settings use the `REOLINK_` prefix and also have matching CLI flags:

| Environment Variable | CLI Flag | Default |
| :--- | :--- | :--- |
| `REOLINK_MQTT_BROKER` | `--mqtt-broker` | `""` |
| `REOLINK_MQTT_USERNAME` | `--mqtt-username` | `""` |
| `REOLINK_MQTT_PASSWORD` | `--mqtt-password` | `""` |
| `REOLINK_MQTT_TOPIC` | `--mqtt-topic` | `reolinkproxy` |
| `REOLINK_SERVER_RTSP_ADDRESS` | `--server-rtsp-address` | `:8554` |
| `REOLINK_SERVER_RTP_ADDRESS` | `--server-rtp-address` | `:8000` |
| `REOLINK_SERVER_RTCP_ADDRESS` | `--server-rtcp-address` | `:8001` |
| `REOLINK_SERVER_ONVIF_ADDRESS` | `--server-onvif-address` | `:8002` |
| `REOLINK_SERVER_ADVERTISE_HOST` | `--server-advertise-host` | auto |
| `REOLINK_SERVER_LOG_PACKETS` | `--server-log-packets` | `false` |
| `REOLINK_ONVIF_USERNAME` | `--onvif-username` | `""` |
| `REOLINK_ONVIF_PASSWORD` | `--onvif-password` | `""` |

## Docker Compose

```yaml
services:
  reolinkproxy:
    image: ghcr.io/shareed2k/reolinkproxy:latest
    container_name: reolinkproxy
    restart: unless-stopped
    network_mode: host
    environment:
      - REOLINK_CAMERA_0_NAME=front
      - REOLINK_CAMERA_0_HOST=192.168.1.100
      - REOLINK_CAMERA_0_USERNAME=admin
      - REOLINK_CAMERA_0_PASSWORD=your_camera_password
      - REOLINK_CAMERA_0_STREAM=main,sub
      - REOLINK_CAMERA_0_CHANNEL=0

      # Example UID/P2P camera instead of HOST:
      # - REOLINK_CAMERA_1_NAME=garage
      # - REOLINK_CAMERA_1_UID=95270DSD7FFRVTAS7
      # - REOLINK_CAMERA_1_USERNAME=admin
      # - REOLINK_CAMERA_1_PASSWORD=your_camera_password

      - REOLINK_ONVIF_USERNAME=admin
      - REOLINK_ONVIF_PASSWORD=secret_onvif_password

      - REOLINK_MQTT_BROKER=tcp://192.168.1.50:1883
      - REOLINK_MQTT_USERNAME=your_mqtt_user
      - REOLINK_MQTT_PASSWORD=your_mqtt_password
      - REOLINK_MQTT_TOPIC=reolinkproxy
```

If you are not using `network_mode: host`, map these ports:

* `8554/tcp` RTSP
* `8000/udp` RTP
* `8001/udp` RTCP
* `8002/tcp` ONVIF
* `3702/udp` WS-Discovery

## CLI Example

The camera list is env-driven. CLI flags are mainly for global settings.

```bash
REOLINK_CAMERA_0_NAME=front \
REOLINK_CAMERA_0_HOST=192.168.1.100 \
REOLINK_CAMERA_0_USERNAME=admin \
REOLINK_CAMERA_0_PASSWORD=secret \
REOLINK_CAMERA_0_STREAM=main,sub \
REOLINK_ONVIF_USERNAME=admin \
REOLINK_ONVIF_PASSWORD=secret \
./reolinkproxy --server-advertise-host 192.168.1.50
```

For more flag details:

```bash
./reolinkproxy --help
```

## Usage with VMS / NVRs

### Frigate

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

### Home Assistant

1. Add the ONVIF integration.
2. Enter the proxy IP.
3. Use port `8002`.
4. Use `REOLINK_ONVIF_USERNAME` and `REOLINK_ONVIF_PASSWORD`.

### MQTT

If `REOLINK_MQTT_BROKER` is set, the proxy publishes and listens on topics under `REOLINK_MQTT_TOPIC`.

Examples:

* Motion status: `reolinkproxy/<CAMERANAME>/status/motion`
* Battery query: `reolinkproxy/<CAMERANAME>/query/battery`
* PTZ control: `reolinkproxy/<CAMERANAME>/control/ptz`
* Siren control: `reolinkproxy/<CAMERANAME>/control/siren`

## Building from Source

```bash
git clone https://github.com/shareed2k/reolinkproxy.git
cd reolinkproxy
go build -o reolinkproxy ./cmd/reolinkproxy
```

Run it with env vars:

```bash
REOLINK_CAMERA_0_NAME=front \
REOLINK_CAMERA_0_HOST=192.168.1.100 \
REOLINK_CAMERA_0_USERNAME=admin \
REOLINK_CAMERA_0_PASSWORD=secret \
./reolinkproxy
```

## License

MIT. See [LICENSE](LICENSE).

## Donations

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/M4M81XYVKG)
