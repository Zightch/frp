package control

import (
	"log/slog"

	"github.com/zightch/frp/frps/internal/control/wiring"
	"github.com/zightch/frp/frps/internal/storage"
)

type (
	Options                 = wiring.Options
	Server                  = wiring.Server
	Logger                  = wiring.Logger
	Repository              = wiring.Repository
	SQLRepository           = wiring.SQLRepository
	GroupRuntime            = wiring.GroupRuntime
	ConfigSnapshot          = wiring.ConfigSnapshot
	UDPListener             = wiring.UDPListener
	ListenKey               = wiring.ListenKey
	BindKind                = wiring.BindKind
	ListenerBind            = wiring.ListenerBind
	ListenerFactory         = wiring.ListenerFactory
	ScriptedListenerFactory = wiring.ScriptedListenerFactory
	ListenerCall            = wiring.ListenerCall
	ScriptedListenerFailure = wiring.ScriptedListenerFailure
)

var ErrGroupNotFound = wiring.ErrGroupNotFound

const (
	BindKindRuntimeProbe = wiring.BindKindRuntimeProbe
	BindKindRuntimeStart = wiring.BindKindRuntimeStart
)

func NewServer(options Options, logger *slog.Logger, version string) *Server {
	return wiring.NewServer(options, logger, version)
}

func NewRepository(store *storage.SQL) *SQLRepository {
	return wiring.NewRepository(store)
}

func NewNetListenerFactory() ListenerFactory {
	return wiring.NewNetListenerFactory()
}

func NewScriptedListenerFactory() *ScriptedListenerFactory {
	return wiring.NewScriptedListenerFactory()
}
