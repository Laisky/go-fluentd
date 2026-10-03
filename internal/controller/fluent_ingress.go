package controller

import (
 "gofluentd/internal/recvs"
 gutils "github.com/Laisky/go-utils"
)

// fluentIngressConfig wires the service settings to the same finite limits
// used by direct receiver construction. Durations require units, e.g. "30s".
func fluentIngressConfig(name string)recvs.FluentIngressCfg{
 p:="settings.acceptor.recvs.plugins."+name+"."
 return recvs.FluentIngressCfg{
  MaxConnections:gutils.Settings.GetInt(p+"max_connections"),
  IdleTimeout:gutils.Settings.GetDuration(p+"idle_timeout"),
  FrameTimeout:gutils.Settings.GetDuration(p+"frame_timeout"),
  MaxFrameBytes:gutils.Settings.GetInt(p+"max_frame_bytes"),
  MaxValueBytes:gutils.Settings.GetInt(p+"max_value_bytes"),
  MaxContainerElements:gutils.Settings.GetInt(p+"max_container_elements"),
  MaxValues:gutils.Settings.GetInt(p+"max_values"),
  MaxDepth:gutils.Settings.GetInt(p+"max_depth"),
 }
}
