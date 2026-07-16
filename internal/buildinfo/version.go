package buildinfo

// Version is the single source of truth for the AXIS release string.
const Version = "0.14.4"

// UpdateManagedBy specifies if this binary is managed by a package manager (e.g. "nix", "homebrew").
// When set, the internal `axis update` command will refuse to overwrite the binary.
var UpdateManagedBy string
