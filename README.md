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
* Can pause streams or stop preview sessions when cameras are idle.
* Supports RTSP talkback publish endpoints that bridge client audio into Baichuan two-way audio.

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
* `TALK_PROFILE`
* `TALK_VOLUME`
* `TALK_ENCODER`
* `TALK_ENCODER_CMD`
* `PAUSE_ON_MOTION`
* `PAUSE_ON_CLIENT`
* `PAUSE_TIMEOUT`
* `IDLE_DISCONNECT`
* `IDLE_TIMEOUT`
* `BATTERY_CAMERA`

Camera defaults:

* `PORT=9000`
* `STREAM=main`
* `TIMEOUT=10s`
* `RTSP_PATH=<NAME>/stream`
* `PAUSE_TIMEOUT=1s`
* `IDLE_TIMEOUT=30s`
* `TALK_VOLUME=100`
* `TALK_ENCODER=internal`

Pause and lifecycle options:

* `PAUSE_ON_CLIENT=true` pauses RTSP packet publishing when no RTSP client is actively playing the stream.
* `PAUSE_ON_MOTION=true` pauses RTSP packet publishing after motion has been inactive for `PAUSE_TIMEOUT`.
* `IDLE_DISCONNECT=true` stops the underlying Baichuan preview session after the stream has been idle for `IDLE_TIMEOUT`.
* `BATTERY_CAMERA=true` uses a much longer reconnect backoff for sleeping cameras. Set `IDLE_DISCONNECT=true` separately if you want idle preview sessions to stop.

Talkback options:

* `TALK_PROFILE=sub` prefers that camera profile for the clean RTSP alias and ONVIF profile ordering.
* This is useful when `main` is H.265 and `sub` is H.264, since some clients are more stable with talkback on the H.264 profile.
* `TALK_ENCODER=internal` is the default and is recommended for Reolink Argus battery cameras.
* `TALK_ENCODER=gstreamer` is available as an explicit opt-in, but some cameras may go silent after a few seconds.

`PAUSE_ON_MOTION` only affects cameras that support the Baichuan motion listener. If motion is unsupported, the stream stays active and MQTT motion state is not published for that camera.

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

Docker healthcheck settings:

| Environment Variable | Healthcheck Flag | Default |
| :--- | :--- | :--- |
| `REOLINK_HEALTHCHECK_RTSP_ADDRESS` | `healthcheck --rtsp-address` | `REOLINK_SERVER_RTSP_ADDRESS` or `:8554` |
| `REOLINK_HEALTHCHECK_PATHS` | `healthcheck --paths` | derived from `REOLINK_CAMERA_<n>_*` |
| `REOLINK_HEALTHCHECK_TIMEOUT` | `healthcheck --timeout` | `5s` |
| `REOLINK_HEALTHCHECK_RTSP_ONLY` | `healthcheck --rtsp-only` | `false` |

By default the Docker image runs `reolinkproxy healthcheck`, which sends RTSP `DESCRIBE` requests to the configured stream paths. Set `REOLINK_HEALTHCHECK_RTSP_ONLY=true` for sleeping battery cameras if you only want to verify that the RTSP listener is up.

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
      - REOLINK_CAMERA_0_TALK_PROFILE=sub
      - REOLINK_CAMERA_0_CHANNEL=0
      - REOLINK_CAMERA_0_PAUSE_ON_CLIENT=true
      - REOLINK_CAMERA_0_IDLE_DISCONNECT=true
      - REOLINK_CAMERA_0_IDLE_TIMEOUT=30s

      # Example battery UID/P2P camera instead of HOST:
      # - REOLINK_CAMERA_1_NAME=garage
      # - REOLINK_CAMERA_1_UID=95270DSD7FFRVTAS7
      # - REOLINK_CAMERA_1_USERNAME=admin
      # - REOLINK_CAMERA_1_PASSWORD=your_camera_password
      # - REOLINK_CAMERA_1_BATTERY_CAMERA=true
      # - REOLINK_CAMERA_1_PAUSE_ON_MOTION=true
      # - REOLINK_CAMERA_1_PAUSE_TIMEOUT=2s

      - REOLINK_ONVIF_USERNAME=admin
      - REOLINK_ONVIF_PASSWORD=secret_onvif_password

      - REOLINK_MQTT_BROKER=tcp://192.168.1.50:1883
      - REOLINK_MQTT_USERNAME=your_mqtt_user
      - REOLINK_MQTT_PASSWORD=your_mqtt_password
      - REOLINK_MQTT_TOPIC=reolinkproxy
    healthcheck:
      test: ["CMD", "/usr/local/bin/reolinkproxy", "healthcheck"]
      interval: 30s
      timeout: 5s
      start_period: 30s
      retries: 3
```

If you are not using `network_mode: host`, map these ports:

* `8554/tcp` RTSP
* `8000/udp` RTP
* `8001/udp` RTCP
* `8002/tcp` ONVIF
* `3702/udp` WS-Discovery

## Docker Run

You can also run the proxy directly using `docker run`:

The container image includes GStreamer, so `REOLINK_CAMERA_<n>_TALK_ENCODER=gstreamer` works without installing anything else in the container. The default is the built-in encoder because it is more stable with battery cameras.

```bash
docker run -d \
  --name reolinkproxy \
  --network host \
  --restart unless-stopped \
  -e REOLINK_CAMERA_0_NAME=front \
  -e REOLINK_CAMERA_0_HOST=192.168.1.100 \
  -e REOLINK_CAMERA_0_USERNAME=admin \
  -e REOLINK_CAMERA_0_PASSWORD=your_camera_password \
  -e REOLINK_CAMERA_0_STREAM=main,sub \
  -e REOLINK_CAMERA_0_TALK_PROFILE=sub \
  -e REOLINK_CAMERA_0_IDLE_DISCONNECT=true \
  -e REOLINK_CAMERA_0_IDLE_TIMEOUT=30s \
  -e REOLINK_ONVIF_USERNAME=admin \
  -e REOLINK_ONVIF_PASSWORD=secret_onvif_password \
  ghcr.io/shareed2k/reolinkproxy:latest
```

## CLI Example

The camera list is env-driven. CLI flags are mainly for global settings.

```bash
REOLINK_CAMERA_0_NAME=front \
REOLINK_CAMERA_0_HOST=192.168.1.100 \
REOLINK_CAMERA_0_USERNAME=admin \
REOLINK_CAMERA_0_PASSWORD=secret \
REOLINK_CAMERA_0_STREAM=main,sub \
REOLINK_CAMERA_0_TALK_PROFILE=sub \
REOLINK_CAMERA_0_IDLE_DISCONNECT=true \
REOLINK_CAMERA_0_IDLE_TIMEOUT=30s \
REOLINK_ONVIF_USERNAME=admin \
REOLINK_ONVIF_PASSWORD=secret \
./reolinkproxy --server-advertise-host 192.168.1.50
```

For more flag details:

```bash
./reolinkproxy --help
```

## Two-Way Audio

Each playable stream has a normal video/audio path and a separate playable two-way path:

* `<STREAM_PATH>`
* `<STREAM_PATH>_twoway`

The normal path does not advertise the RTSP backchannel. Use it for always-on detect/record clients such as Frigate ffmpeg. Use the `_twoway` path only for live-view clients that should expose microphone/talkback.

Each camera also exposes a dedicated RTSP talkback publish path:

* `<RTSP_PATH>_talk`

Examples:

* Camera stream path: `front/stream`
* Two-way playable path: `rtsp://<PROXY_IP>:8554/front/stream_twoway`
* Talkback publish path: `rtsp://<PROXY_IP>:8554/front/stream_talk`

The current implementation accepts RTSP `ANNOUNCE` / `SETUP` / `RECORD` publishers with:

* mono `PCMU`
* mono `PCMA`

The proxy decodes G.711, resamples as needed, encodes the camera's required ADPCM talk format, and forwards it over Baichuan.

Example with GStreamer:

```bash
gst-launch-1.0 \
  autoaudiosrc ! audioconvert ! audioresample ! audio/x-raw,rate=8000,channels=1 \
  ! mulawenc ! rtppcmupay pt=0 \
  ! rtspclientsink location=rtsp://<PROXY_IP>:8554/front/stream_talk protocols=tcp
```

Current limitation:

* the ONVIF service advertises a Profile T audio backchannel, enabling 2-way audio in clients like Scrypted and Frigate/go2rtc.
* for multi-profile cameras, set `REOLINK_CAMERA_<n>_TALK_PROFILE=sub` if you want the clean `RTSP_PATH` alias and ONVIF default profile to prefer the sub stream for talkback.

Frigate/go2rtc direct RTSP example:

```yaml
cameras:
  front:
    ffmpeg:
      inputs:
        - path: rtsp://127.0.0.1:8554/front
          input_args: preset-rtsp-restream
          roles:
            - detect
            - record
            - audio
    live:
      streams:
        Stream: front
        Two Way: front_twoway

go2rtc:
  streams:
    front:
      - rtsp://<PROXY_IP>:8554/front/stream
    front_twoway:
      - rtsp://<PROXY_IP>:8554/front/stream_twoway
```

## Usage with VMS / NVRs

### go2rtc

You can use `go2rtc` to provide a WebRTC interface with 2-way talk using the ONVIF backchannel.

Add the camera using the ONVIF URL:

```yaml
streams:
  office: "onvif://admin:secret_onvif_password@<PROXY_IP>:8002"
```

Because the proxy correctly advertises ONVIF Profile T audio outputs, `go2rtc` will automatically discover the backchannel and expose the WebRTC microphone button in its web interface.

If your `main` profile is H.265 and WebRTC talkback freezes video, prefer the H.264 sub profile:

```yaml
environment:
  - REOLINK_CAMERA_0_STREAM=main,sub
  - REOLINK_CAMERA_0_TALK_PROFILE=sub
```

That keeps explicit `..._main` and `..._sub` paths, but makes the clean `RTSP_PATH` alias and ONVIF profile ordering prefer `sub`.

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

If you provide an `MQTT_BROKER`, the proxy will automatically connect and expose real-time topics:
* **Auto-Discovery**: Natively registers a Motion Sensor in Home Assistant.
* **Motion Status**: Publishes `on` / `off` to `reolinkproxy/<CAMERANAME>/status/motion`.
* **Battery Queries**: Send an empty payload to `reolinkproxy/<CAMERANAME>/query/battery` to instantly get `%` and JSON status.
* **Remote PTZ**: Send `left`, `right`, `up`, `down` to `reolinkproxy/<CAMERANAME>/control/ptz`.
* **Siren**: Send `on` to `reolinkproxy/<CAMERANAME>/control/siren` to instantly trigger the camera alarm.

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
REOLINK_CAMERA_0_PAUSE_ON_CLIENT=true \
REOLINK_CAMERA_0_IDLE_DISCONNECT=true \
./reolinkproxy
```

## License

MIT. See [LICENSE](LICENSE).

## Donations

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/M4M81XYVKG)
