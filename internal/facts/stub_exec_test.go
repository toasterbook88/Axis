package facts

import "context"

type stubExec struct {
	runs []string
}

func (s *stubExec) Connect(context.Context) error { return nil }
func (s *stubExec) Close() error                  { return nil }
func (s *stubExec) Run(_ context.Context, cmd string) (string, error) {
	s.runs = append(s.runs, cmd)
	return "Linux\n", nil
}
func (s *stubExec) RunWithStdin(ctx context.Context, cmd string, stdin []byte) (string, error) {
	return s.Run(ctx, cmd)
}
