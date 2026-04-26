package api

import (
	"time"

	"github.com/zightch/frp/frps/internal/api/groupconfig"
	apisettings "github.com/zightch/frp/frps/internal/api/settings"
)

const (
	schemaTimestampLayout = groupconfig.SchemaTimestampLayout

	proxyGroupStatusEnabled  = groupconfig.ProxyGroupStatusEnabled
	proxyGroupStatusDisabled = groupconfig.ProxyGroupStatusDisabled
	proxyGroupStatusAbnormal = groupconfig.ProxyGroupStatusAbnormal

	proxyGroupStatusReasonMissingLocalIP      = groupconfig.ProxyGroupStatusReasonMissingLocalIP
	proxyGroupStatusReasonSnapshotUnavailable = groupconfig.ProxyGroupStatusReasonSnapshotUnavailable

	tunnelStatusEnabled  = groupconfig.TunnelStatusEnabled
	tunnelStatusDisabled = groupconfig.TunnelStatusDisabled
	tunnelStatusConflict = groupconfig.TunnelStatusConflict
	tunnelStatusAbnormal = groupconfig.TunnelStatusAbnormal

	entryCertificatesPathSettings = apisettings.EntryCertificatesPath
)

func schemaTimestamp() string {
	return time.Now().UTC().Format(schemaTimestampLayout)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
