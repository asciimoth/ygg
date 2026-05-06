package tunnative

import (
	gtun "github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/ygg/ygglib/logger"
)

type Logger = logger.Logger

type Config struct {
	Name    string
	Address string
	MTU     uint64
	FD      int32
}

func Create(log Logger, cfg Config) (gtun.Tun, error) {
	return create(log, cfg)
}
