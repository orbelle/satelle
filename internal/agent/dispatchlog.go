package agent

import (
	"path/filepath"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/integration/common"
	"github.com/bobmcallan/satelle/internal/logsread"
)

func closeLog(l *common.Log, path string) {
	if l == nil {
		return
	}
	l.Close()
	_ = logsread.PruneDispatch(filepath.Dir(filepath.Dir(path)), config.DefaultLogsMaxFiles)
}
