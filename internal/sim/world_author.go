package sim

// AuthorID is the assignment target the sweep uses (DESIGN §3): the
// first configured author identity, or "" when the project maps none.
func (w *World) AuthorID() string {
	if ids := w.Config.Actors["author"]; len(ids) > 0 {
		return ids[0]
	}
	return ""
}
