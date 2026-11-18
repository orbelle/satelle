//go:build !darwin

package cli

func unitPath() string { return userUnitPath(serviceUnitName) }

func syncUnitPath() string { return userUnitPath(syncTimerName) }

func daemonUnitDesc() string { return serviceUnitName }

func syncUnitDesc() string { return syncTimerName }

func installedDaemon(body string) string { return unitDaemon(body) }
