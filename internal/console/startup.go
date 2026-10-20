package console

// ModelState is the startup state of the active model as the footer shows it.
// It is defined here, ahead of the start-screen behavior that drives it, so the
// footer contract stays stable while that behavior lands.
type ModelState int

const (
	// ModelReady means a model is bound; the footer shows its name. It is the
	// zero value so a Model that never learns a state behaves as it always did.
	ModelReady ModelState = iota
	// ModelPending means a model is still being resolved; the footer shows the
	// spinner frame in place of a name.
	ModelPending
	// ModelNone means resolution finished and found no model.
	ModelNone
)
