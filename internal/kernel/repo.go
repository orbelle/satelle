package kernel

import (
	"errors"
	"fmt"
	"slices"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

func (k *Kernel) RepoRegistered(name string) bool {
	return slices.Contains(k.Repos.Targets(), name)
}

func (k *Kernel) registeredRepos() string { return reporeg.Literals(k.Repos.Targets()) }

func (k *Kernel) checkRepo(name string) error {
	if name == "" {
		return errors.New("a repo is named: self, or a repository .satelle/" + reporeg.TrackedFile + " registers")
	}
	if !k.RepoRegistered(name) {
		return fmt.Errorf("repo %s is not registered in .satelle/%s (registered: %s)", reporeg.Literal(name), reporeg.TrackedFile, k.registeredRepos())
	}
	return nil
}

func (k *Kernel) checkRepoChange(it item.Item, events []ledger.Event, name string) error {
	if w, ok := k.Workflows[it.Workflow]; ok && w.Part != "" && name != it.Target() {
		return fmt.Errorf("item %s is on workflow %s of repository %s; its repo cannot change to %s: create the item again with --repo %s",
			it.ID, reporeg.Literal(w.Name), reporeg.Literal(w.Part), reporeg.Literal(name), reporeg.Literal(name))
	}
	if !item.HeldSeat(events) {
		return k.checkRepo(name)
	}
	if name != it.Target() {
		return fmt.Errorf("item %s has held a seat, so its repo is set: it targets %s; a repo is set once, before the first seat", it.ID, reporeg.Literal(it.Target()))
	}
	return nil
}

func (k *Kernel) CheckTarget(it item.Item) error {
	if k.RepoRegistered(it.Target()) {
		return nil
	}
	return fmt.Errorf("item %s targets repo %s, which .satelle/%s does not register (registered: %s); register it with satelle repo add before working on it",
		it.ID, reporeg.Literal(it.Target()), reporeg.TrackedFile, k.registeredRepos())
}

func (k *Kernel) CheckTargets(ids ...string) error {
	for _, id := range ids {
		it, err := k.Get(id)
		if err != nil {
			return err
		}
		if err := k.CheckTarget(it); err != nil {
			return err
		}
	}
	return nil
}

func (k *Kernel) CheckFire(id string) error {
	it, err := k.Get(id)
	if err != nil {
		return err
	}
	if err := k.CheckTarget(it); err != nil {
		return err
	}
	if it.Inputs == nil {
		return nil
	}
	inputs, err := k.TaskInputs(id)
	if err != nil {
		return err
	}
	return k.CheckTargets(inputs...)
}
