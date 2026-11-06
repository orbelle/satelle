package workflow

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// Queue is one queues/<name>.toml: the definition of a queue (title, filter,
// trigger). The ledger keeps the queue's membership and fires on the chain the
// file binds: ID when set (adopting an existing queue), else <prefix>_<name>.
type Queue struct {
	Name    string          `toml:"-"`
	File    string          `toml:"-"`
	ID      string          `toml:"id"`
	Title   string          `toml:"title"`
	Filter  *item.Selection `toml:"filter"`
	Trigger *item.Trigger   `toml:"trigger"`
}

var queueName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ParseQueue reads one queue file. Its name is the file's stem.
func ParseQueue(path string) (*Queue, error) {
	q := &Queue{}
	if err := DecodeStrict(path, q); err != nil {
		return nil, err
	}
	q.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	q.File = path
	return q, nil
}

// LoadQueues reads every *.toml in dir, keyed by name. A missing directory is
// no queues.
func LoadQueues(dir string) (map[string]*Queue, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := map[string]*Queue{}
	var errs []error
	for _, p := range paths {
		q, err := ParseQueue(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out[q.Name] = q
	}
	return out, errors.Join(errs...)
}

// Validate checks the file's own shape; workflowExists answers whether a
// trigger's workflow is configured. A trigger task's existence is ledger
// state, checked when the queue fires.
func (q *Queue) Validate(workflowExists func(string) bool) []error {
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", q.File, fmt.Sprintf(format, a...)))
	}
	if !queueName.MatchString(q.Name) {
		bad("queue name %q (the file's name) must be lower-case letters, digits and dashes", q.Name)
	}
	if strings.TrimSpace(q.Title) == "" {
		bad("title is required")
	}
	if q.ID != "" && !ledger.ValidItem(q.ID) {
		bad("id %q is not an item id", q.ID)
	}
	if q.Filter != nil && *q.Filter == (item.Selection{}) {
		bad("[filter] is empty: name at least one of kind, status, tag, category, or remove it (no filter = explicit adds only)")
	}
	if q.Trigger != nil {
		if err := q.Trigger.Validate(); err != nil {
			bad("[trigger]: %v", err)
		} else if q.Trigger.Workflow != "" && !workflowExists(q.Trigger.Workflow) {
			bad("[trigger] workflow %q is not configured", q.Trigger.Workflow)
		}
	}
	return errs
}

// ChainID is the ledger chain the file binds.
func (q *Queue) ChainID(prefix string) string {
	if q.ID != "" {
		return q.ID
	}
	return prefix + "_" + q.Name
}

// Apply sets the file's definition (title, filter, trigger) on it, the
// queue's folded item. It is the only place a definition meets an item.
func (q *Queue) Apply(it *item.Item) {
	it.Title = q.Title
	it.Inputs, it.Trigger = nil, nil
	if q.Filter != nil {
		f := *q.Filter
		it.Inputs = &f
	}
	if q.Trigger != nil {
		t := *q.Trigger
		it.Trigger = &t
	}
}
