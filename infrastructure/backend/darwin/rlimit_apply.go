package darwin

import "syscall"

// rlimitOps abstracts getrlimit/setrlimit so the save/apply/restore logic is
// testable on any GOOS with an in-memory double (rlimit_apply_test.go). The
// real syscall-backed implementation lives behind the darwin build tag in
// runner_darwin.go.
type rlimitOps interface {
	Getrlimit(resource int) (*syscall.Rlimit, error)
	Setrlimit(resource int, lim *syscall.Rlimit) error
}

// rlimitSaveAndApply captures the current limit for each lowered resource,
// applies the lowers, and returns the previous values keyed by resource. If
// applying a lower fails partway, the resources already lowered are rolled back
// to their captured previous values so the caller is never left with a
// partially-lowered, permanently-mutated limit. An empty lowers slice is a
// no-op returning an empty map.
func rlimitSaveAndApply(lowers []Rlimit, ops rlimitOps) (map[int]syscall.Rlimit, error) {
	prev := make(map[int]syscall.Rlimit, len(lowers))
	for _, rl := range lowers {
		lim, err := ops.Getrlimit(rl.Resource)
		if err != nil {
			return nil, err
		}
		prev[rl.Resource] = *lim
	}

	applied := make([]int, 0, len(lowers))
	for _, rl := range lowers {
		if err := ops.Setrlimit(rl.Resource, &syscall.Rlimit{Cur: rl.Soft, Max: rl.Hard}); err != nil {
			for _, res := range applied {
				if p, ok := prev[res]; ok {
					_ = ops.Setrlimit(res, &p)
				}
			}

			return nil, err
		}
		applied = append(applied, rl.Resource)
	}

	return prev, nil
}

// rlimitRestore sets each captured resource back to its previous value, so the
// caller's limits are restored after the child inherits the lowered ones at
// fork. A Set error is propagated to the caller.
func rlimitRestore(prev map[int]syscall.Rlimit, ops rlimitOps) error {
	for res, lim := range prev {
		if err := ops.Setrlimit(res, &lim); err != nil {
			return err
		}
	}

	return nil
}
