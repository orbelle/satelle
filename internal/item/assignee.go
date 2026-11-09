package item

import "slices"

// AssigneesOf is an item's assignees: users lists the explicit assignees in
// their order, then each user holding one of seats not already listed, in
// bind order, each once; working lists, in the same order, the users of users
// that any of seats holds. seats are the item's live seats: the caller drops
// the ones proven orphaned. A seat that records no user adds no one. Both
// results are non-nil.
func AssigneesOf(assigned []string, seats []Seat) (users, working []string) {
	users, working = []string{}, []string{}
	seated := map[string]bool{}
	for _, s := range seats {
		if s.User != "" {
			seated[s.User] = true
		}
	}
	add := func(u string) {
		if u == "" || slices.Contains(users, u) {
			return
		}
		users = append(users, u)
		if seated[u] {
			working = append(working, u)
		}
	}
	for _, u := range assigned {
		add(u)
	}
	for _, s := range seats {
		add(s.User)
	}
	return users, working
}
