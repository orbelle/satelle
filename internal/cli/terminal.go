package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// openTerminal opens the controlling terminal for reading and writing. It is a
// variable so tests can stand in a terminal.
var openTerminal = func() (io.ReadWriteCloser, error) {
	return os.OpenFile("/dev/tty", os.O_RDWR, 0)
}

// confirmOnTerminal asks the person at the controlling terminal to retype the
// item id before the operator's word on it is recorded. It never reads stdin:
// an agent's tool call has no terminal of its own, and when it shares the
// operator's, only the person at the keyboard can answer.
func confirmOnTerminal(id, state, typ, to string) error {
	tty, err := openTerminal()
	if err != nil {
		return errors.New("no controlling terminal: the operator records from their own terminal")
	}
	defer tty.Close()
	fmt.Fprintf(tty, "Recording the operator's word on %s (%s, type %s): move to %s. Type the item id to confirm: ", id, state, typ, to)
	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("no answer on the controlling terminal: nothing recorded")
	}
	if strings.TrimSpace(line) != id {
		return fmt.Errorf("the id typed on the terminal is not %s: nothing recorded", id)
	}
	return nil
}
