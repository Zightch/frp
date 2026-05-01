package ratepolicy

func NewService(options Options) *Service {
	if options.Store == nil {
		return nil
	}

	return &Service{
		store:     options.Store,
		refresher: options.RuntimeRefresher,
	}
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
