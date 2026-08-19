# Reolink Proxy

A lightweight Go proxy that translates Reolink's proprietary Baichuan protocol into standard RTSP streams and a compliant ONVIF API.

It is aimed at battery Reolink cameras and other models that do not expose native RTSP/ONVIF, or that are reachable on the LAN by IP or by Reolink UID (local UDP discovery on the same network segment).

## Features

* Connects to cameras by local IP (TCP) or Reolink UID (LAN broadcast discovery on the same subnet).
* Repackages H.264/H.265 video to RTSP without video transcoding.
* Transcodes Reolink ADPCM audio to PCMA and passes AAC through.
* Exposes ONVIF `Device`, `Media`, `PTZ`, `Events`, `Imaging`, `Analytics`, `Recording`, and `Search` services with WS-Security auth support.
* JPEG snapshots straight from the camera at `http://<host>:8002/api/snapshot/<rtsp path>` (also advertised via ONVIF `GetSnapshotUri`; protected by Basic auth when ONVIF credentials are set). Note: on battery cameras a snapshot wakes the camera.
* ONVIF PTZ: continuous move, stop, preset recall, absolute zoom, and emulated `RelativeMove` (enables Frigate autotracking; calibrate with `REOLINK_CAMERA_<n>_PTZ_RELATIVE_MS_PER_UNIT`, default 1000ms for a full-range move).
* ONVIF pull-point events: motion plus AI detections (person, vehicle, pet, visitor) on Reolink-native topics for Home Assistant and NVRs.
* ONVIF imaging: brightness/saturation/contrast/sharpness read and write.
* ONVIF recording search: lists SD-card clips per camera (FindRecordings/FindEvents); replay streaming over ONVIF is not implemented yet.
* RTCP Sender Reports with camera-anchored NTP for client A/V sync (disable with `REOLINK_SERVER_DISABLE_RTCP_SENDER_REPORTS=true` for legacy clients).
* Broadcasts WS-Discovery for local ONVIF discovery.
* Supports multiple streams per camera: `main`, `sub`, and `extern` (mid-tier ext).
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

### Connecting by UID (local LAN only)

`REOLINK_CAMERA_<n>_UID` uses **UDP broadcast discovery** on ports **2015** and **2018**. The camera must be on the **same L2 broadcast domain** as the proxy (same VLAN/subnet, or the proxy host has a NIC on that segment). The proxy does **not**:

* search other subnets or routed networks (e.g. proxy on `10.x` and camera on `30.x` will not discover via UID),
* scan IP ranges,
* use Reolink cloud / remote P2P relay.

If discovery fails you will see `uid discovery timed out` (default wait: `TIMEOUT=10s`, overridable with `REOLINK_CAMERA_<n>_TIMEOUT`).

**Cross-subnet or known IP:** use `REOLINK_CAMERA_<n>_HOST` instead (Baichuan TCP on port **9000**). Ensure the proxy host can reach the camera (firewall allows TCP 9000). If both `HOST` and `UID` are set, **HOST wins** and UID discovery is not used.

**Docker:** for UID mode on a home LAN, use `network_mode: host` so broadcasts leave the host stack (bridged/NAT Docker often blocks discovery, similar to ONVIF WS-Discovery).

Example (current env layout — replace older flat `REOLINK_UID` / `REOLINK_STREAM` vars from early images):

```yaml
environment:
  - REOLINK_CAMERA_0_NAME=backyard
  - REOLINK_CAMERA_0_UID=ABCDEFGHIJKLMNOP
  - REOLINK_CAMERA_0_USERNAME=admin
  - REOLINK_CAMERA_0_PASSWORD=secret
  - REOLINK_CAMERA_0_STREAM=main,sub
  - REOLINK_ONVIF_USERNAME=admin
  - REOLINK_ONVIF_PASSWORD=secret_onvif_password
```

Cross-subnet example:

```yaml
environment:
  - REOLINK_CAMERA_0_NAME=backyard
  - REOLINK_CAMERA_0_HOST=10.10.30.23
  - REOLINK_CAMERA_0_USERNAME=admin
  - REOLINK_CAMERA_0_PASSWORD=secret
```

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

### Stream profiles

Set `REOLINK_CAMERA_<n>_STREAM` to a comma-separated list of profile names. The proxy pulls live video over Baichuan (port 9000), not camera FLV or RTSP URLs.

| Profile | Baichuan | Typical Reolink equivalent | Role |
| :--- | :--- | :--- | :--- |
| `main` | `mainStream` | `channel0_main.bcs`, main RTSP | Highest resolution (often H.265) |
| `extern` | `externStream` | `channel0_ext.bcs`, FLV ext URL | Mid-tier sub (e.g. doorbell ~896x672, H.264) |
| `sub` | `subStream` | `channel0_sub.bcs`, `Preview_01_sub` | Lowest sub (e.g. 640x480) |

With `RTSP_PATH=doorbell/stream` and `STREAM=main,sub,extern`:

* `rtsp://<PROXY_IP>:8554/doorbell/stream_main`
* `rtsp://<PROXY_IP>:8554/doorbell/stream_sub`
* `rtsp://<PROXY_IP>:8554/doorbell/stream_extern`

If `TALK_PROFILE` is set to one of the configured profiles, that profile also gets a clean alias at `doorbell/stream` (same as today for `sub`). Example: `TALK_PROFILE=sub` keeps talkback on the low sub stream while you point detect at `doorbell/stream_extern`.

#### RTSP URL layout (`main` + `sub`)

With `NAME=voorkant`, default `RTSP_PATH=voorkant/stream`, `STREAM=main,sub`, and `TALK_PROFILE=sub`:

| URL | Profile | Notes |
| :--- | :--- | :--- |
| `rtsp://<PROXY_IP>:8554/voorkant/stream_main` | main | Highest resolution |
| `rtsp://<PROXY_IP>:8554/voorkant/stream_sub` | sub | Lower resolution |
| `rtsp://<PROXY_IP>:8554/voorkant/stream` | sub | Alias of `TALK_PROFILE` (not main) |
| `rtsp://<PROXY_IP>:8554/voorkant/stream_twoway` | sub | Same video as `…/stream`, plus RTSP backchannel |
| `rtsp://<PROXY_IP>:8554/voorkant/stream_main_twoway` | main | Main with backchannel |
| `rtsp://<PROXY_IP>:8554/voorkant/stream_sub_twoway` | sub | Sub with backchannel |

Common misconception: with `TALK_PROFILE=sub`, **`…/stream` is not the main stream** — use `…/stream_main` for record/detect at full resolution.

**`_twoway` does not pick main vs sub.** It only adds two-way audio on the **same** profile as the path it suffixes. `stream` and `stream_twoway` therefore share the same resolution when both are aliases of `sub`.

Set `TALK_PROFILE=main` to alias `voorkant/stream` and `voorkant/stream_twoway` to main instead (some clients struggle with H.265 main for talkback; explicit `…/stream_main` and `…/stream_sub` paths always work).

`extern` resolution and FPS are fixed by camera firmware (not configurable in the Reolink app). Not every model exposes `externStream`; if preview fails, omit `extern` and use `sub` only.

#### Doorbell / higher-resolution detect

Many doorbells expose a higher-resolution mid stream via FLV `channel0_ext.bcs` while native RTSP sub (`Preview_01_sub`) stays at 640x480. Use the `extern` profile instead of pulling FLV directly:

```bash
REOLINK_CAMERA_0_STREAM=main,sub,extern
REOLINK_CAMERA_0_RTSP_PATH=doorbell/stream
REOLINK_CAMERA_0_TALK_PROFILE=sub
```

Point detect/record clients at `rtsp://<PROXY_IP>:8554/doorbell/stream_extern`. Confirm resolution in proxy logs (`info size=...`) or with `ffprobe` on that URL.

Frigate example:

```yaml
cameras:
  doorbell:
    ffmpeg:
      inputs:
        - path: rtsp://127.0.0.1:8554/doorbell/stream_extern
          roles: [detect]
```

To use the ext stream as the default alias (no `_extern` suffix in client URLs):

```bash
REOLINK_CAMERA_0_STREAM=main,extern
REOLINK_CAMERA_0_TALK_PROFILE=extern
```

Then `rtsp://<PROXY_IP>:8554/doorbell/stream` is the ext tier.

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
| `REOLINK_SERVER_VIDEO_PACER_INITIAL_LATENCY_MS` | `--server-video-pacer-initial-latency-ms` | `1500` |
| `REOLINK_SERVER_VIDEO_PACER_MAX_LEAD_MS` | `--server-video-pacer-max-lead-ms` | `3000` |
| `REOLINK_SERVER_VIDEO_PACER_SNAP_ON_PAST` | `--server-video-pacer-snap-on-past` | `false` |
| `REOLINK_SERVER_AUDIO_PACER_INITIAL_LATENCY_MS` | `--server-audio-pacer-initial-latency-ms` | `500` |
| `REOLINK_SERVER_AUDIO_PACER_MAX_LEAD_MS` | `--server-audio-pacer-max-lead-ms` | `2000` |
| `REOLINK_SERVER_AUDIO_PACER_SNAP_ON_PAST` | `--server-audio-pacer-snap-on-past` | `true` |
| `REOLINK_ONVIF_USERNAME` | `--onvif-username` | `""` |
| `REOLINK_ONVIF_PASSWORD` | `--onvif-password` | `""` |

### Latency tuning

The proxy paces media onto RTSP clients to smooth the bursty Baichuan delivery
(which otherwise causes DTS jitter in downstream recorders). The pacing adds
end-to-end latency: the video pacer starts `1500ms` behind the first frame,
and its cursor may run up to `3000ms` ahead of the wall clock before
re-anchoring, so cameras whose clock runs slightly fast accumulate up to that
much extra delay. If you prefer lower latency over maximum smoothing (e.g.
live viewing instead of recording), reduce both:

```yaml
environment:
  - REOLINK_SERVER_VIDEO_PACER_INITIAL_LATENCY_MS=200
  - REOLINK_SERVER_VIDEO_PACER_MAX_LEAD_MS=500
  - REOLINK_SERVER_AUDIO_PACER_INITIAL_LATENCY_MS=200
  - REOLINK_SERVER_AUDIO_PACER_MAX_LEAD_MS=500
```

Values near zero minimize proxy-added latency but reintroduce upstream burst
jitter, which can bother strict consumers (ffmpeg recording, Frigate VOD).

Docker healthcheck settings:

| Environment Variable | Healthcheck Flag | Default |
| :--- | :--- | :--- |
| `REOLINK_HEALTHCHECK_RTSP_ADDRESS` | `healthcheck --rtsp-address` | `REOLINK_SERVER_RTSP_ADDRESS` or `:8554` |
| `REOLINK_HEALTHCHECK_PATHS` | `healthcheck --paths` | derived from `REOLINK_CAMERA_<n>_*` |
| `REOLINK_HEALTHCHECK_TIMEOUT` | `healthcheck --timeout` | `5s` |
| `REOLINK_HEALTHCHECK_RTSP_ONLY` | `healthcheck --rtsp-only` | `false` |
| `REOLINK_HEALTHCHECK_MAX_PACKET_AGE` | `healthcheck --max-packet-age` | `0` (disabled) |
| `REOLINK_HEALTHCHECK_ONVIF_ADDRESS` | `healthcheck --onvif-address` | `REOLINK_SERVER_ONVIF_ADDRESS` or `:8002` |

By default the Docker image runs `reolinkproxy healthcheck`, which sends RTSP `DESCRIBE` requests to the configured stream paths. Set `REOLINK_HEALTHCHECK_RTSP_ONLY=true` for sleeping battery cameras if you only want to verify that the RTSP listener is up.

Set `REOLINK_HEALTHCHECK_MAX_PACKET_AGE` (e.g. `30s`) to additionally fail the healthcheck when a stream that has active RTSP clients has not delivered a video packet within the given duration. This catches a stalled camera session — the proxy is connected and RTSP still answers `DESCRIBE`, but no frames flow — so Docker/orchestrators can restart the container automatically. The check queries the proxy's `GET /healthz?max_video_age=<duration>` endpoint on the ONVIF listener; you can also probe that endpoint directly from external monitoring.

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
      # Main stream: rtsp://<host>:8554/front/stream_main (not …/stream when TALK_PROFILE=sub)
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

Each playable stream profile has a normal path and a `_twoway` variant on **that same profile** (see [RTSP URL layout](#rtsp-url-layout-main--sub) above). The `_twoway` suffix enables the RTSP backchannel; it does **not** switch from main to sub.

* `<STREAM_PATH>` — playback without backchannel
* `<STREAM_PATH>_twoway` — same resolution/codec, plus microphone/talkback

With `STREAM=main,sub` and `TALK_PROFILE=sub`, both `front/stream` and `front/stream_twoway` are the **sub** profile. Use `front/stream_main` or `front/stream_main_twoway` for main.

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
* **Auto-Discovery**: Registers a Home Assistant device per camera with these entities: motion sensor, siren switch, reboot button, privacy-mode switch, auto-focus switch, and — after the first successful battery read — a battery level sensor (polled every 10 minutes).
* **Motion Status**: Publishes `on` / `off` to `reolinkproxy/<CAMERANAME>/status/motion`.
* **Battery**: Level published to `reolinkproxy/<CAMERANAME>/status/battery_level`; send an empty payload to `reolinkproxy/<CAMERANAME>/query/battery` for an instant JSON status.
* **Remote PTZ**: Send `left`, `right`, `up`, `down` to `reolinkproxy/<CAMERANAME>/control/ptz`. Movement continues until you send `stop`. Append a speed to control how fast the camera moves, e.g. `left 10` (default `32`).
* **PTZ presets**: Send the preset ID (e.g. `0`, `1`) to `reolinkproxy/<CAMERANAME>/control/ptz/preset` to move the camera to a saved preset.
* **Siren**: Send `on` / `off` to `reolinkproxy/<CAMERANAME>/control/siren` to trigger or stop the camera alarm.
* **Privacy mode**: Send `on` / `off` to `reolinkproxy/<CAMERANAME>/control/privacy`.
* **Auto focus**: Send `on` / `off` to `reolinkproxy/<CAMERANAME>/control/autofocus`.

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
