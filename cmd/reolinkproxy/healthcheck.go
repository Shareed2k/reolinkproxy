package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
)

const defaultHealthcheckTimeout = 5 * time.Second

type rtspHealthTarget struct {
	display     string
	requestURL  string
	dialAddress string
}

func newHealthcheckCommand() *cli.Command {
	var rtspAddress string
	var rawPaths string
	var timeout time.Duration
	var rtspOnly bool

	return &cli.Command{
		Name:  "healthcheck",
		Usage: "check RTSP listener and stream readiness",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "rtsp-address",
				Usage:       "RTSP listen address to probe",
				Sources:     envVars("HEALTHCHECK_RTSP_ADDRESS", "SERVER_RTSP_ADDRESS"),
				Value:       cfg.Server.RTSPAddress,
				Destination: &rtspAddress,
			},
			&cli.StringFlag{
				Name:        "paths",
				Usage:       "comma-separated RTSP paths or URLs to DESCRIBE; defaults to configured camera stream paths",
				Sources:     envVars("HEALTHCHECK_PATHS"),
				Destination: &rawPaths,
			},
			&cli.DurationFlag{
				Name:        "timeout",
				Usage:       "overall probe timeout",
				Sources:     envVars("HEALTHCHECK_TIMEOUT"),
				Value:       defaultHealthcheckTimeout,
				Destination: &timeout,
			},
			&cli.BoolFlag{
				Name:        "rtsp-only",
				Usage:       "only verify that the RTSP TCP listener accepts connections",
				Sources:     envVars("HEALTHCHECK_RTSP_ONLY"),
				Destination: &rtspOnly,
			},
		},
		Action: func(ctx context.Context, _ *cli.Command) error {
			if rtspOnly {
				return checkRTSPPort(ctx, rtspAddress, timeout)
			}

			paths := splitHealthcheckPaths(rawPaths)
			if len(paths) == 0 {
				cameras, err := loadCamerasFromEnv()
				if err != nil {
					return fmt.Errorf("load cameras from environment: %w", err)
				}
				paths = healthcheckPathsForCameras(cameras)
			}

			if len(paths) == 0 {
				return checkRTSPPort(ctx, rtspAddress, timeout)
			}

			return runRTSPHealthcheck(ctx, rtspAddress, paths, timeout)
		},
	}
}

func splitHealthcheckPaths(raw string) []string {
	parts := strings.Split(raw, ",")
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			paths = append(paths, part)
		}
	}
	return paths
}

func healthcheckPathsForCameras(cameras []CameraConfig) []string {
	paths := make([]string, 0)
	seen := make(map[string]struct{})

	for _, camera := range cameras {
		streams := splitCameraStreams(camera.Stream)
		basePath := strings.Trim(camera.RTSPPath, "/")
		for _, stream := range streams {
			path := basePath
			if len(streams) > 1 {
				path = basePath + "_" + stream
			}
			appendUniqueHealthcheckPath(&paths, seen, path)
		}

		if len(streams) > 1 && camera.preferredTalkProfile() != "" {
			appendUniqueHealthcheckPath(&paths, seen, basePath)
		}
	}

	return paths
}

func appendUniqueHealthcheckPath(paths *[]string, seen map[string]struct{}, path string) {
	path = strings.Trim(path, "/")
	if path == "" {
		return
	}
	if _, ok := seen[path]; ok {
		return
	}
	seen[path] = struct{}{}
	*paths = append(*paths, path)
}

func runRTSPHealthcheck(ctx context.Context, rtspAddress string, paths []string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultHealthcheckTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for _, path := range paths {
		target, err := newRTSPHealthTarget(rtspAddress, path)
		if err != nil {
			return err
		}
		if err := describeRTSPTarget(ctx, target); err != nil {
			return err
		}
	}

	return nil
}

func checkRTSPPort(ctx context.Context, rtspAddress string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultHealthcheckTimeout
	}

	dialAddress, err := normalizeRTSPProbeAddress(rtspAddress)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", dialAddress)
	if err != nil {
		return fmt.Errorf("rtsp listener: dial %s: %w", dialAddress, err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	request := fmt.Sprintf("OPTIONS * RTSP/1.0\r\nCSeq: 1\r\nUser-Agent: reolinkproxy-healthcheck/%s\r\n\r\n", Version)
	if _, err := io.WriteString(conn, request); err != nil {
		return fmt.Errorf("rtsp listener: write OPTIONS: %w", err)
	}

	statusLine, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("rtsp listener: read OPTIONS response: %w", err)
	}
	statusLine = strings.TrimSpace(statusLine)

	statusCode, err := parseRTSPStatusCode(statusLine)
	if err != nil {
		return fmt.Errorf("rtsp listener: %w", err)
	}
	if statusCode != 200 {
		return fmt.Errorf("rtsp listener: OPTIONS returned %s", statusLine)
	}

	return nil
}

func newRTSPHealthTarget(rtspAddress string, rawPath string) (rtspHealthTarget, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return rtspHealthTarget{}, fmt.Errorf("empty RTSP healthcheck path")
	}

	if strings.Contains(rawPath, "://") {
		u, err := url.Parse(rawPath)
		if err != nil {
			return rtspHealthTarget{}, fmt.Errorf("parse RTSP healthcheck URL %q: %w", rawPath, err)
		}
		if u.Scheme != "rtsp" {
			return rtspHealthTarget{}, fmt.Errorf("unsupported RTSP healthcheck URL scheme %q", u.Scheme)
		}
		if u.Host == "" {
			return rtspHealthTarget{}, fmt.Errorf("RTSP healthcheck URL %q has no host", rawPath)
		}

		host := u.Hostname()
		port := u.Port()
		if port == "" {
			port = "554"
		}
		dialAddress := net.JoinHostPort(host, port)

		return rtspHealthTarget{
			display:     rawPath,
			requestURL:  u.String(),
			dialAddress: dialAddress,
		}, nil
	}

	dialAddress, err := normalizeRTSPProbeAddress(rtspAddress)
	if err != nil {
		return rtspHealthTarget{}, err
	}

	path := strings.Trim(rawPath, "/")
	u := url.URL{
		Scheme: "rtsp",
		Host:   dialAddress,
		Path:   "/" + path,
	}

	return rtspHealthTarget{
		display:     path,
		requestURL:  u.String(),
		dialAddress: dialAddress,
	}, nil
}

func normalizeRTSPProbeAddress(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = cfg.Server.RTSPAddress
	}
	if raw == "" {
		raw = ":8554"
	}

	var host string
	var port string
	if strings.HasPrefix(raw, ":") {
		port = strings.TrimPrefix(raw, ":")
	} else {
		var err error
		host, port, err = net.SplitHostPort(raw)
		if err != nil {
			if strings.Count(raw, ":") == 0 {
				if _, convErr := strconv.Atoi(raw); convErr == nil {
					port = raw
				} else {
					host = raw
					port = "8554"
				}
			} else {
				return "", fmt.Errorf("invalid RTSP address %q: %w", raw, err)
			}
		}
	}

	if port == "" {
		return "", fmt.Errorf("invalid RTSP address %q: missing port", raw)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	return net.JoinHostPort(host, port), nil
}

func describeRTSPTarget(ctx context.Context, target rtspHealthTarget) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", target.dialAddress)
	if err != nil {
		return fmt.Errorf("%s: dial %s: %w", target.display, target.dialAddress, err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	request := fmt.Sprintf(
		"DESCRIBE %s RTSP/1.0\r\nCSeq: 1\r\nAccept: application/sdp\r\nUser-Agent: reolinkproxy-healthcheck/%s\r\n\r\n",
		target.requestURL,
		Version,
	)
	if _, err := io.WriteString(conn, request); err != nil {
		return fmt.Errorf("%s: write DESCRIBE: %w", target.display, err)
	}

	statusLine, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("%s: read DESCRIBE response: %w", target.display, err)
	}
	statusLine = strings.TrimSpace(statusLine)

	statusCode, err := parseRTSPStatusCode(statusLine)
	if err != nil {
		return fmt.Errorf("%s: %w", target.display, err)
	}
	if statusCode != 200 {
		return fmt.Errorf("%s: DESCRIBE returned %s", target.display, statusLine)
	}

	return nil
}

func parseRTSPStatusCode(statusLine string) (int, error) {
	parts := strings.SplitN(statusLine, " ", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "RTSP/") {
		return 0, fmt.Errorf("invalid RTSP response %q", statusLine)
	}

	statusCode, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid RTSP status %q", statusLine)
	}
	return statusCode, nil
}
