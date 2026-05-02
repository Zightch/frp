package client

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/zightch/frp/frps/pkg/protocol"
)

type clientConnectionState uint8

const (
	connectionStateStartup clientConnectionState = iota
	connectionStateActive
	connectionStateReconnecting
)

func (c *Client) infof(source, format string, args ...any) {
	c.logf(slog.LevelInfo, source, format, args...)
}

func (c *Client) debugf(source, format string, args ...any) {
	c.logf(slog.LevelDebug, source, format, args...)
}

func (c *Client) logf(level slog.Level, source, format string, args ...any) {
	if c == nil || c.logger == nil {
		return
	}
	c.logger.Log(context.Background(), level, fmt.Sprintf(format, args...), slog.String("source", source))
}

func (c *Client) markSessionActive() {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	c.connectState = connectionStateActive
}

func (c *Client) noteReconnect(err error) {
	if c == nil {
		return
	}

	c.logMu.Lock()
	alreadyReconnecting := c.connectState == connectionStateReconnecting
	c.connectState = connectionStateReconnecting
	c.logMu.Unlock()

	if alreadyReconnecting {
		c.debugf("client", "重连失败 error=%v", err)
		return
	}
	if isLoginConflictError(err) {
		c.infof("login", "登录冲突，当前分组已有其他 frpc 在线，重连 frps 中...")
		return
	}
	c.infof("client", "重连 frps 中...")
}

func (c *Client) resetBackendFailures() {
	if c == nil {
		return
	}
	c.logMu.Lock()
	defer c.logMu.Unlock()
	clear(c.backendFailures)
}

func (c *Client) logBackendDialFailure(tunnel protocol.TunnelEntry, target string, err error) {
	if c == nil {
		return
	}

	key := backendFailureKey(tunnel, target)
	c.logMu.Lock()
	_, repeated := c.backendFailures[key]
	c.backendFailures[key] = struct{}{}
	c.logMu.Unlock()

	if repeated {
		c.debugf("backend", "隧道[%s] 连接内网失败 %s error=%v", tunnelDisplayName(tunnel), target, err)
		return
	}
	c.infof("backend", "隧道[%s] 连接内网失败 %s error=%v", tunnelDisplayName(tunnel), target, err)
}

func (c *Client) clearBackendDialFailure(tunnel protocol.TunnelEntry, target string) {
	if c == nil {
		return
	}
	c.logMu.Lock()
	defer c.logMu.Unlock()
	delete(c.backendFailures, backendFailureKey(tunnel, target))
}

func (c *Client) logConfigApplied(push protocol.ConfigPush, summary configReloadSummary) {
	enabled := 0
	for _, tunnel := range push.Tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled != 0 {
			enabled++
		}
	}

	c.infof(
		"config",
		"配置已同步 version=%d 隧道=%d 启用=%d 新增=%d 替换=%d 删除=%d",
		push.ConfigVersion,
		len(push.Tunnels),
		enabled,
		summary.addedTunnels,
		summary.replacedTunnels,
		summary.removedTunnels,
	)

	for _, tunnel := range push.Tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}
		c.infof("proxy", "%s", tunnelReadySummary(tunnel))
	}
}

func tunnelReadySummary(tunnel protocol.TunnelEntry) string {
	message := fmt.Sprintf(
		"隧道[%s] 已就绪 %s %s -> %s tls=%s",
		tunnelDisplayName(tunnel),
		protocolName(tunnel.Protocol),
		portSpan(tunnel.RemoteStart, tunnel.RemoteEnd),
		localTargetSummary(tunnel),
		tlsModeLabel(tunnel.BackendTLSMode),
	)
	if tunnel.BackendTLSMode == protocol.TunnelTLSModeOff {
		return message
	}

	verify := "ca=" + tlsCALabel(tunnel)
	if tunnel.BackendTLSInsecureSkipVerify {
		verify = "verify=skip"
	}
	return fmt.Sprintf(
		"%s %s cert=%s sni=%s",
		message,
		verify,
		yesNo(tunnel.BackendTLSClientCertPEM != ""),
		serverNameLabel(tunnel.BackendTLSServerName),
	)
}

func backendFailureKey(tunnel protocol.TunnelEntry, target string) string {
	return strconv.FormatUint(uint64(tunnel.TunnelID), 10) + "|" + target
}

func tunnelDisplayName(tunnel protocol.TunnelEntry) string {
	name := strings.TrimSpace(tunnel.TunnelName)
	if name != "" {
		return name
	}
	return strconv.FormatUint(uint64(tunnel.TunnelID), 10)
}

func localTargetSummary(tunnel protocol.TunnelEntry) string {
	return tunnel.LocalHost.String() + ":" + portSpan(tunnel.LocalStart, tunnel.LocalEnd)
}

func portSpan(start, end uint16) string {
	if start == end {
		return strconv.Itoa(int(start))
	}
	return strconv.Itoa(int(start)) + "-" + strconv.Itoa(int(end))
}

func tlsModeLabel(mode uint8) string {
	switch mode {
	case protocol.TunnelTLSModeTLS:
		return "tls"
	case protocol.TunnelTLSModeMTLS:
		return "mtls"
	default:
		return "off"
	}
}

func tlsCALabel(tunnel protocol.TunnelEntry) string {
	switch {
	case tunnel.BackendTLSLoadSystemCA && tunnel.BackendTLSCAPEM != "":
		return "system+custom"
	case tunnel.BackendTLSLoadSystemCA:
		return "system"
	case tunnel.BackendTLSCAPEM != "":
		return "custom"
	default:
		return "none"
	}
}

func serverNameLabel(serverName string) string {
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		return "-"
	}
	return serverName
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
