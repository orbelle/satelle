package verb

import (
	"context"
	"database/sql"
	"errors"

	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// ErrStoreNotConfigured is returned when a verb runs before its backing store
// was wired. The in-process bootstrap calls the SetXStore functions below at
// startup; a nil store means a wiring bug, not a user error.
var ErrStoreNotConfigured = errors.New("verb: store not configured")

// Package-global stores, wired by the bootstrap (app.Open → SetXStore). Verbs
// read these; a nil handle yields ErrStoreNotConfigured. Globals mirror
// satellites' SetXStore pattern — the single seam both CLI and web wire once.
var (
	workItemStore *workitem.Store
	ledgerStore   *ledger.Store
	docIndexStore *docindex.Store
	leaseStore    *lease.Store
	// txRunner runs a func inside one sqlite transaction spanning Stories and
	// Ledger. Required whenever ledgerStore is set: a missing runner refuses
	// the transition rather than writing the two sides separately.
	txRunner TxRunner
)

// SetWorkItemStore wires the stories/tasks store. Pass nil to reset (tests).
func SetWorkItemStore(s *workitem.Store) { workItemStore = s }

// SetLedgerStore wires the evidence-ledger store.
func SetLedgerStore(s *ledger.Store) { ledgerStore = s }

// TxRunner runs fn inside one database transaction.
type TxRunner func(ctx context.Context, fn func(*sql.Tx) error) error

// SetTxRunner wires the transaction runner used to enact a status change and
// its status_transition append atomically. Pass nil to reset (tests).
func SetTxRunner(r TxRunner) { txRunner = r }

// SetDocIndexStore wires the authored-doc index store.
func SetDocIndexStore(s *docindex.Store) { docIndexStore = s }

// SetLeaseStore wires the engagement-lease store (sty_8426b9c0).
func SetLeaseStore(s *lease.Store) { leaseStore = s }

// Realtime change topics — coarse, panel-level. A mutating verb publishes one
// after it commits so an open web page refetches just that panel.
const (
	TopicStories = "stories"
	TopicTasks   = "tasks"
	TopicDocs    = "docs"
)

// changeNotifiers are invoked after a mutating verb commits, with a topic
// constant above. Multiple sinks share the seam (sty_126228b2): the web SSE
// hub and the CLI→server change publisher both register here. Empty on the
// plain CLI path with no [server] endpoint makes notifyChange a no-op.
// Package globals mirror the store wiring and keep verb free of transport imports.
var changeNotifiers []func(topic string)

// SetChangeNotifier replaces the notifier list with a single sink (or clears
// it when fn is nil). Prefer AddChangeNotifier when composing sinks.
func SetChangeNotifier(fn func(topic string)) {
	if fn == nil {
		changeNotifiers = nil
		return
	}
	changeNotifiers = []func(topic string){fn}
}

// AddChangeNotifier appends a sink on the shared change seam (e.g. the push
// publisher beside an existing SSE hub). Nil is ignored.
func AddChangeNotifier(fn func(topic string)) {
	if fn == nil {
		return
	}
	changeNotifiers = append(changeNotifiers, fn)
}

// notifyChange fires every registered notifier.
func notifyChange(topic string) {
	for _, fn := range changeNotifiers {
		fn(topic)
	}
}

// requireWorkItem returns the wired store or ErrStoreNotConfigured.
func requireWorkItem() (*workitem.Store, error) {
	if workItemStore == nil {
		return nil, ErrStoreNotConfigured
	}
	return workItemStore, nil
}

func requireLedger() (*ledger.Store, error) {
	if ledgerStore == nil {
		return nil, ErrStoreNotConfigured
	}
	return ledgerStore, nil
}

func requireDocIndex() (*docindex.Store, error) {
	if docIndexStore == nil {
		return nil, ErrStoreNotConfigured
	}
	return docIndexStore, nil
}

func requireLease() (*lease.Store, error) {
	if leaseStore == nil {
		return nil, ErrStoreNotConfigured
	}
	return leaseStore, nil
}
