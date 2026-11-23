package store

import "gitea.deepak.science/deepak/taiga/internal/metrics"

func (s *pgStore) PoolStats() metrics.PoolStats {
	st := s.pgPool.Stat()
	return metrics.PoolStats{
		Max:      int(st.MaxConns()),
		InUse:    int(st.AcquiredConns()),
		Idle:     int(st.IdleConns()),
		Waits:    st.EmptyAcquireCount(),
		WaitTime: st.EmptyAcquireWaitTime(),
	}
}

func (s *sqliteStore) PoolStats() metrics.PoolStats {
	st := s.db.Stats()
	return metrics.PoolStats{
		Max:      st.MaxOpenConnections,
		InUse:    st.InUse,
		Idle:     st.Idle,
		Waits:    st.WaitCount,
		WaitTime: st.WaitDuration,
	}
}

func (m *errorStore) PoolStats() metrics.PoolStats { return metrics.PoolStats{} }
