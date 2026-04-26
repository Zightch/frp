package groupconfig

import (
	"context"
	"fmt"

	"github.com/zightch/frp/frps/internal/proxygroups"
	"github.com/zightch/frp/frps/internal/settings/entrycerts"
	"github.com/zightch/frp/frps/internal/system"
)

func NewService(options Options) *Service {
	if options.Store == nil {
		return nil
	}

	return &Service{
		store:      options.Store,
		network:    options.Network,
		refresher:  options.RuntimeRefresher,
		runtime:    options.RuntimeStatus,
		entryCerts: entrycerts.NewService(options.Store, entrycerts.ServiceOptions{}),
	}
}

func (s *Service) ListLocalIPs() ([]LocalIPView, error) {
	if s == nil || s.network == nil {
		return nil, &Error{Status: 503, Message: "local network snapshot is unavailable"}
	}

	snapshot := s.network.Current()
	items := make([]LocalIPView, 0, len(snapshot.AvailableIPs)+2)
	items = append(items,
		LocalIPView{Addr: "0.0.0.0", Family: "ipv4", Standard: "0.0.0.0"},
		LocalIPView{Addr: "::", Family: "ipv6", Standard: "::"},
	)

	for _, ip := range snapshot.AvailableIPs {
		normalized, err := system.NormalizeListenIP(ip.Addr)
		if err != nil {
			continue
		}
		items = append(items, LocalIPView{
			Addr:     ip.Addr,
			Family:   string(ip.Family),
			Standard: normalized,
		})
	}

	return items, nil
}

func (s *Service) CountTLSRequiredProxyGroups(ctx context.Context) (int64, error) {
	if s == nil || s.store == nil {
		return 0, fmt.Errorf("management store is unavailable")
	}
	row, err := s.store.QueryOneContext(
		ctx,
		`SELECT COUNT(1) AS count FROM proxy_groups WHERE control_transport_security = ?`,
		string(proxygroups.ControlTransportSecurityTLSRequired),
	)
	if err != nil {
		return 0, fmt.Errorf("count tls required proxy groups: %w", err)
	}
	return rowInt64(row, "count")
}

func (s *Service) refreshGroups(groupIDs ...int64) {
	if s == nil || s.refresher == nil {
		return
	}

	seen := make(map[int64]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID <= 0 {
			continue
		}
		if _, ok := seen[groupID]; ok {
			continue
		}
		seen[groupID] = struct{}{}
		s.refresher.RefreshGroup(groupID)
	}
}
