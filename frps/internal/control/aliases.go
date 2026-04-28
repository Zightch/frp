package control

import (
	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	controlrepo "github.com/zightch/frp/frps/internal/control/repo"
	"github.com/zightch/frp/frps/internal/storage"
)

type (
	Repository              = controlrepo.Repository
	SQLRepository           = controlrepo.SQLRepository
	GroupRuntime            = controlrepo.GroupRuntime
	ConfigSnapshot          = controlrepo.ConfigSnapshot
	UDPListener             = controlbind.UDPListener
	ListenKey               = controlbind.ListenKey
	BindKind                = controlbind.BindKind
	ListenerBind            = controlbind.ListenerBind
	ListenerFactory         = controlbind.ListenerFactory
	ScriptedListenerFactory = controlbind.ScriptedListenerFactory
	ListenerCall            = controlbind.ListenerCall
	ScriptedListenerFailure = controlbind.ScriptedListenerFailure
)

var ErrGroupNotFound = controlrepo.ErrGroupNotFound

const (
	BindKindRuntimeProbe = controlbind.BindKindRuntimeProbe
	BindKindRuntimeStart = controlbind.BindKindRuntimeStart
)

func NewRepository(store *storage.SQL) *SQLRepository {
	return controlrepo.NewSQLRepository(store)
}

func NewNetListenerFactory() ListenerFactory {
	return controlbind.NewNetListenerFactory()
}

func NewScriptedListenerFactory() *ScriptedListenerFactory {
	return controlbind.NewScriptedListenerFactory()
}
