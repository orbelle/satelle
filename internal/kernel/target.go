package kernel

import "fmt"

type MappedTargetError struct {
	Target  string
	Dir     string
	Control string
}

func (e *MappedTargetError) Error() string {
	return fmt.Sprintf("a kernel for target %s of the control repository %s opens read-only: satelle reads a mapped target work tree, it does not write from one; run the command from the control repository", e.Target, e.Control)
}

func RefuseAtTarget(o Options) error {
	if o.Target == "" || o.ReadOnly != "" {
		return nil
	}
	return &MappedTargetError{Target: o.Target, Dir: o.Dir, Control: o.Root}
}
