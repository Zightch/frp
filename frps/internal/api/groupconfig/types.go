package groupconfig

import (
	"net/http"

	"github.com/zightch/frp/frps/internal/proxygroups"
	"github.com/zightch/frp/frps/internal/settings/entrycerts"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
)

const SchemaTimestampLayout = "2006-01-02 15:04:05.000000"

const (
	ProxyGroupStatusEnabled  = "启用"
	ProxyGroupStatusDisabled = "禁用"
	ProxyGroupStatusAbnormal = "异常"

	ProxyGroupStatusReasonMissingLocalIP      = "已配置 IP 当前不存在于本机"
	ProxyGroupStatusReasonSnapshotUnavailable = "本机 IP 列表暂不可用"

	TunnelStatusEnabled  = "启用"
	TunnelStatusDisabled = "禁用"
	TunnelStatusConflict = "冲突"
	TunnelStatusAbnormal = "异常"
)

const schemaTimestampLayout = SchemaTimestampLayout

const (
	proxyGroupStatusEnabled  = ProxyGroupStatusEnabled
	proxyGroupStatusDisabled = ProxyGroupStatusDisabled
	proxyGroupStatusAbnormal = ProxyGroupStatusAbnormal

	proxyGroupStatusReasonMissingLocalIP      = ProxyGroupStatusReasonMissingLocalIP
	proxyGroupStatusReasonSnapshotUnavailable = ProxyGroupStatusReasonSnapshotUnavailable

	tunnelStatusEnabled  = TunnelStatusEnabled
	tunnelStatusDisabled = TunnelStatusDisabled
	tunnelStatusConflict = TunnelStatusConflict
	tunnelStatusAbnormal = TunnelStatusAbnormal
)

type GroupRuntimeRefresher interface {
	RefreshGroup(groupID int64)
}

type TunnelRuntimeStatusReader interface {
	TunnelRuntimeIssues() map[int64]string
}

type Options struct {
	Store            *storage.SQL
	Network          system.SnapshotReader
	RuntimeRefresher GroupRuntimeRefresher
	RuntimeStatus    TunnelRuntimeStatusReader
}

type Service struct {
	store      *storage.SQL
	network    system.SnapshotReader
	refresher  GroupRuntimeRefresher
	runtime    TunnelRuntimeStatusReader
	entryCerts *entrycerts.Service
}

type Error struct {
	Status  int
	Message string
	Code    string
	Details any
}

type ProxyGroupView struct {
	ID                       int64  `json:"id"`
	Name                     string `json:"name"`
	ClientID                 string `json:"client_id"`
	EffectiveIP              string `json:"effective_ip"`
	Enabled                  bool   `json:"enabled"`
	ControlTransportSecurity string `json:"control_transport_security"`
	Status                   string `json:"status"`
	StatusReason             string `json:"status_reason,omitempty"`
	CreatedAt                string `json:"created_at"`
	UpdatedAt                string `json:"updated_at"`
}

type TunnelView struct {
	ID                           int64   `json:"id"`
	GroupID                      int64   `json:"group_id"`
	GroupName                    string  `json:"group_name"`
	Name                         string  `json:"name"`
	Protocol                     string  `json:"protocol"`
	RemoteType                   string  `json:"remote_type"`
	RemoteStart                  int64   `json:"remote_start"`
	RemoteEnd                    int64   `json:"remote_end"`
	LocalHost                    string  `json:"local_host"`
	LocalStart                   int64   `json:"local_start"`
	LocalEnd                     int64   `json:"local_end"`
	ListenTLSMode                string  `json:"listen_tls_mode"`
	ListenTLSLoadSystemCA        bool    `json:"listen_tls_load_system_ca"`
	ListenTLSServerCertAssetID   *int64  `json:"listen_tls_server_cert_asset_id,omitempty"`
	ListenTLSClientCAAssetIDs    []int64 `json:"listen_tls_client_ca_asset_ids,omitempty"`
	BackendTLSMode               string  `json:"backend_tls_mode"`
	BackendTLSServerName         string  `json:"backend_tls_server_name,omitempty"`
	BackendTLSLoadSystemCA       bool    `json:"backend_tls_load_system_ca"`
	BackendTLSInsecureSkipVerify bool    `json:"backend_tls_insecure_skip_verify"`
	BackendTLSClientCertAssetID  *int64  `json:"backend_tls_client_cert_asset_id,omitempty"`
	BackendTLSCAAssetIDs         []int64 `json:"backend_tls_ca_asset_ids,omitempty"`
	Enabled                      bool    `json:"enabled"`
	Status                       string  `json:"status"`
	StatusReason                 string  `json:"status_reason,omitempty"`
	CreatedAt                    string  `json:"created_at"`
	UpdatedAt                    string  `json:"updated_at"`

	GroupEffectiveIP string `json:"-"`
	GroupEnabled     bool   `json:"-"`
}

type ProxyGroupCreateRequest struct {
	Name                     string `json:"name"`
	EffectiveIP              string `json:"effective_ip"`
	Enabled                  *bool  `json:"enabled"`
	ControlTransportSecurity string `json:"control_transport_security"`
}

type ProxyGroupPatchRequest struct {
	Name                     *string `json:"name"`
	EffectiveIP              *string `json:"effective_ip"`
	Enabled                  *bool   `json:"enabled"`
	ControlTransportSecurity *string `json:"control_transport_security"`
}

type TunnelRequest struct {
	GroupID                      int64   `json:"group_id"`
	Name                         string  `json:"name"`
	Protocol                     string  `json:"protocol"`
	RemoteType                   string  `json:"remote_type"`
	RemoteStart                  int64   `json:"remote_start"`
	RemoteEnd                    int64   `json:"remote_end"`
	LocalHost                    string  `json:"local_host"`
	LocalStart                   int64   `json:"local_start"`
	LocalEnd                     int64   `json:"local_end"`
	Enabled                      *bool   `json:"enabled"`
	ListenTLSMode                *string `json:"listen_tls_mode"`
	ListenTLSLoadSystemCA        *bool   `json:"listen_tls_load_system_ca"`
	ListenTLSServerCertAssetID   *int64  `json:"listen_tls_server_cert_asset_id"`
	ListenTLSClientCAAssetIDs    []int64 `json:"listen_tls_client_ca_asset_ids"`
	BackendTLSMode               *string `json:"backend_tls_mode"`
	BackendTLSServerName         *string `json:"backend_tls_server_name"`
	BackendTLSLoadSystemCA       *bool   `json:"backend_tls_load_system_ca"`
	BackendTLSInsecureSkipVerify *bool   `json:"backend_tls_insecure_skip_verify"`
	BackendTLSClientCertAssetID  *int64  `json:"backend_tls_client_cert_asset_id"`
	BackendTLSCAAssetIDs         []int64 `json:"backend_tls_ca_asset_ids"`
}

type LocalIPView struct {
	Addr     string `json:"addr"`
	Family   string `json:"family"`
	Standard string `json:"standard"`
}

type normalizedProxyGroup struct {
	Name                     string
	EffectiveIP              string
	Enabled                  bool
	ControlTransportSecurity proxygroups.ControlTransportSecurity
}

type normalizedTunnel struct {
	GroupID                      int64
	Name                         string
	Protocol                     string
	RemoteType                   string
	RemoteStart                  int64
	RemoteEnd                    int64
	LocalHost                    string
	LocalStart                   int64
	LocalEnd                     int64
	Enabled                      bool
	ListenTLSMode                tunnelTLSMode
	ListenTLSLoadSystemCA        bool
	ListenTLSServerCertAssetID   *int64
	ListenTLSClientCAAssetIDs    []int64
	BackendTLSMode               tunnelTLSMode
	BackendTLSServerName         string
	BackendTLSLoadSystemCA       bool
	BackendTLSInsecureSkipVerify bool
	BackendTLSClientCertAssetID  *int64
	BackendTLSCAAssetIDs         []int64
}

type TunnelMutationResult struct {
	Item     TunnelView
	Warnings []string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *Error) HTTPStatus() int {
	if e == nil || e.Status == 0 {
		return http.StatusInternalServerError
	}
	return e.Status
}

func (e *Error) HTTPCode() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func (e *Error) HTTPDetails() any {
	if e == nil {
		return nil
	}
	return e.Details
}
