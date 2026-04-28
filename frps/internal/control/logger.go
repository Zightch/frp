package control

type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}
