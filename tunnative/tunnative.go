package tunnative

import gtun "github.com/asciimoth/gonnect/tun"

type Logger interface {
	Printf(string, ...interface{})
	Infof(string, ...interface{})
	Warnf(string, ...interface{})
	Warnln(...interface{})
	Errorf(string, ...interface{})
	Errorln(...interface{})
}

type Config struct {
	Name    string
	Address string
	MTU     uint64
	FD      int32
}

func Create(log Logger, cfg Config) (gtun.Tun, error) {
	return create(log, cfg)
}
