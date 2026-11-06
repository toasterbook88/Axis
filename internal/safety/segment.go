package safety

import "strings"

// segment is one command, prepared for rule matching.
type segment struct {
	program    string
	joinedArgs string   // arguments matched by ArgPatterns
	lowerCmd   string   // the raw segment, lowercased, for RawPatterns
	workDirs   []string // lowercased directories git runs in (-C, --git-dir, --work-tree, GIT_DIR, GIT_WORK_TREE)
}

// newSegment prepares a parsed command. For git, ArgPatterns see the
// subcommand and its arguments after git's global options, so
// "git -C /repo push" matches the git-push rule; the directories those
// options name are kept for WorkDirPatterns. For service managers, leading
// options are skipped, so "systemctl --user restart x" matches. Other
// programs keep their arguments as parsed.
func newSegment(raw, program string, args []string) segment {
	s := segment{program: program, lowerCmd: strings.ToLower(raw)}
	switch program {
	case "git":
		args, s.workDirs = gitSubcommand(args)
		s.workDirs = append(s.workDirs, gitEnvDirs(raw)...)
	case "systemctl", "launchctl", "service":
		for len(args) > 0 && strings.HasPrefix(args[0], "-") {
			args = args[1:]
		}
	}
	s.joinedArgs = strings.Join(args, " ")
	return s
}

// gitSubcommand skips git's global options and returns the subcommand with
// its arguments, and the lowercased directories -C, --git-dir and
// --work-tree name (separate or attached values).
func gitSubcommand(args []string) ([]string, []string) {
	var dirs []string
	i := 0
	for i < len(args) {
		tok := args[i]
		switch {
		case tok == "-C" || tok == "--git-dir" || tok == "--work-tree":
			if i+1 < len(args) {
				dirs = append(dirs, strings.ToLower(args[i+1]))
			}
			i += 2
		case tok == "-c":
			i += 2
		case strings.HasPrefix(tok, "--git-dir=") || strings.HasPrefix(tok, "--work-tree="):
			dirs = append(dirs, strings.ToLower(tok[strings.Index(tok, "=")+1:]))
			i++
		case strings.HasPrefix(tok, "-C") && len(tok) > 2:
			dirs = append(dirs, strings.ToLower(tok[2:]))
			i++
		case strings.HasPrefix(tok, "-"):
			i++ // --no-pager, -p, --bare, -cKEY=VAL and other value-less globals
		default:
			return args[i:], dirs
		}
	}
	return nil, dirs
}

// gitEnvDirs returns the lowercased GIT_DIR and GIT_WORK_TREE values from the
// segment's leading environment assignments.
func gitEnvDirs(raw string) []string {
	var dirs []string
	for _, tok := range shellSplit(strings.TrimSpace(raw)) {
		key, val, ok := strings.Cut(tok, "=")
		if !ok || strings.HasPrefix(tok, "-") {
			break
		}
		if key == "GIT_DIR" || key == "GIT_WORK_TREE" {
			dirs = append(dirs, strings.ToLower(val))
		}
	}
	return dirs
}

// anywhere expands option prefixes into ArgPatterns that match the option as
// the first argument or after any other argument.
func anywhere(opts ...string) []string {
	out := make([]string, 0, 2*len(opts))
	for _, o := range opts {
		out = append(out, o+"*", "* "+o+"*")
	}
	return out
}
