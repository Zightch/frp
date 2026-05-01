package ratepolicy

import (
	"net/http"

	"github.com/zightch/frp/frps/internal/storage"
)

const SchemaTimestampLayout = "2006-01-02 15:04:05.000000"

type GroupRuntimeRefresher interface {
	RefreshGroup(groupID int64)
}

type Options struct {
	Store            *storage.SQL
	RuntimeRefresher GroupRuntimeRefresher
}

type Service struct {
	store     *storage.SQL
	refresher GroupRuntimeRefresher
}

type Error struct {
	Status  int
	Message string
	Code    string
	Details any
}

type RateValueRequest struct {
	Value int64  `json:"value"`
	Unit  string `json:"unit"`
}

type RatePolicyRequest struct {
	Name     string           `json:"name"`
	Mode     string           `json:"mode"`
	Downlink RateValueRequest `json:"downlink"`
	Uplink   RateValueRequest `json:"uplink"`
}

type RatePolicyBindingRequest struct {
	TunnelID int64 `json:"tunnel_id"`
}

type RatePolicyView struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Mode         string `json:"mode"`
	DownlinkBPS  int64  `json:"downlink_bps"`
	UplinkBPS    int64  `json:"uplink_bps"`
	BindingCount int64  `json:"binding_count"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type RatePolicyBindingView struct {
	ID           int64  `json:"id"`
	RatePolicyID int64  `json:"rate_policy_id"`
	TunnelID     int64  `json:"tunnel_id"`
	GroupID      int64  `json:"group_id"`
	GroupName    string `json:"group_name"`
	TunnelName   string `json:"tunnel_name"`
	Protocol     string `json:"protocol"`
	RemoteType   string `json:"remote_type"`
	RemoteStart  int64  `json:"remote_start"`
	RemoteEnd    int64  `json:"remote_end"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type normalizedRatePolicy struct {
	Name        string
	Mode        string
	DownlinkBPS int64
	UplinkBPS   int64
}

type bindingTunnel struct {
	ID          int64
	GroupID     int64
	GroupName   string
	Name        string
	Protocol    string
	RemoteType  string
	RemoteStart int64
	RemoteEnd   int64
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
