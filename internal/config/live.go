package config

import "sync/atomic"

// Live 是「当前生效的配置」。
//
// 热生效的做法是：各处**每次用时**从这里取，而不是启动时把值拷进自己的字段。
// 拷成字段的那一刻，这个配置项就变成需重启的了——而使用者从界面上看不出区别。
type Live struct {
	v atomic.Pointer[Config]
}

func NewLive(c Config) *Live {
	l := &Live{}
	l.v.Store(&c)
	return l
}

func (l *Live) Get() Config { return *l.v.Load() }

func (l *Live) Set(c Config) { l.v.Store(&c) }
