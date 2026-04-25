package proxygroups

import "strings"

type ControlTransportSecurity string

const (
	ControlTransportSecurityPlain       ControlTransportSecurity = "plain"
	ControlTransportSecurityTLSRequired ControlTransportSecurity = "tls_required"
)

func NormalizeControlTransportSecurity(value string) ControlTransportSecurity {
	switch ControlTransportSecurity(strings.ToLower(strings.TrimSpace(value))) {
	case ControlTransportSecurityPlain:
		return ControlTransportSecurityPlain
	case ControlTransportSecurityTLSRequired:
		return ControlTransportSecurityTLSRequired
	default:
		return ""
	}
}

func DefaultControlTransportSecurity() ControlTransportSecurity {
	return ControlTransportSecurityPlain
}
