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

type Queue struct {
	Name    string          `toml:"-"`
	File    string          `toml:"-"`
	ID      string          `toml:"id"`
	Title   string          `toml:"title"`
	Filter  *item.Selection `toml:"filter"`
	Trigger *item.Trigger   `toml:"trigger"`
}

var queueName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func ParseQueue(path string) (*Queue, error) {
	q := &Queue{}
	if err := DecodeStrict(path, q); err != nil {
		return nil, err
	}
	q.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	q.File = path
	return q, nil
}

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

func (q *Queue) ChainID(prefix string) string {
	if q.ID != "" {
		return q.ID
	}
	return prefix + "_" + q.Name
}

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

func (q *Queue) Fields() item.Fields {
	title := q.Title
	in, t := item.Selection{}, item.Trigger{}
	if q.Filter != nil {
		in = *q.Filter
	}
	if q.Trigger != nil {
		t = *q.Trigger
	}
	return item.Fields{Title: &title, Inputs: &in, Trigger: &t}
}

func (q *Queue) Differs(it item.Item) bool {
	want := it
	q.Apply(&want)
	return it.Title != want.Title || !samePtr(it.Inputs, want.Inputs) || !samePtr(it.Trigger, want.Trigger)
}

func samePtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
