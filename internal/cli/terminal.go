package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

var openTerminal = func() (io.ReadWriteCloser, error) {
	return os.OpenFile("/dev/tty", os.O_RDWR, 0)
}

func terminalAttached() bool {
	tty, err := openTerminal()
	if err != nil {
		return false
	}
	_ = tty.Close()
	return true
}

func confirmOnTerminal(id, state, typ string) error {
	tty, err := openTerminal()
	if err != nil {
		return errors.New("no controlling terminal: the operator records from their own terminal")
	}
	defer tty.Close()
	fmt.Fprintf(tty, "Recording the operator's word on %s (%s, type %s). Type the item id to confirm: ", id, state, typ)
	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("no answer on the controlling terminal: nothing recorded")
	}
	if strings.TrimSpace(line) != id {
		return fmt.Errorf("the id typed on the terminal is not %s: nothing recorded", id)
	}
	return nil
}
