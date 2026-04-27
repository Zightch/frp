package app

import "github.com/zightch/frp/frps/internal/api"

type groupRuntimeRefreshFanout struct {
	targets []api.GroupRuntimeRefresher
}

func (f groupRuntimeRefreshFanout) RefreshGroup(groupID int64) {
	if groupID <= 0 {
		return
	}

	for _, target := range f.targets {
		if target == nil {
			continue
		}
		target.RefreshGroup(groupID)
	}
}
