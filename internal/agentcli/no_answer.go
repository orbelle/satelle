package agentcli

import "errors"

// ErrNoAnswer marks a run whose agent settled or exited without a final
// answer (sty_7bfa980d). It is how a transport says "the model did not
// respond", so the dispatcher can move a binding's model list on to the next
// model; it carries no message of its own (see Mark).
var ErrNoAnswer = errors.New("the agent settled without an answer")

// Mark returns err with mark added to its chain and its message unchanged, so
// errors.Is(Mark(err, mark), mark) holds while the text a user reads stays
// exactly err's. Nil err stays nil.
func Mark(err, mark error) error {
	if err == nil {
		return nil
	}
	return markedError{err: err, mark: mark}
}

type markedError struct{ err, mark error }

func (m markedError) Error() string   { return m.err.Error() }
func (m markedError) Unwrap() []error { return []error{m.err, m.mark} }
