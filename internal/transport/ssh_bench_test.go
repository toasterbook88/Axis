package transport

import (
	"context"
	"testing"
)

// BenchmarkSSHConnectionReuse quantifies the benefit of reusing an SSH
// connection across multiple commands vs creating a new connection per
// command.  This provides evidence for the Phase-G decision on whether
// to implement cross-cycle connection pooling.
//
// Current AXIS behaviour (within a single Collect()):
//
//	Connect once → Run ~30 commands → Close once  (matches benchReuse)
//
// Hypothetical non-reuse behaviour:
//
//	Connect → Run → Close × 30  (matches benchNoReuse)
func BenchmarkSSHConnectionReuse(b *testing.B) {
	clientKey, clientSigner := generateTestKeyPair(b)
	_, hostSigner := generateTestKeyPair(b)

	server := startSSHTestServer(b, clientSigner.PublicKey(), hostSigner, map[string]sshCommandResponse{
		"echo hi": {stdout: "hi\n"},
	})
	defer server.Close()

	home := writeSSHClientEnv(b, clientKey, hostSigner, server.Host(), server.Port())
	restore := stubSSHConfigEnvWithOutput(b, home,
		"identityfile ~/.ssh/test_key\nuserknownhostsfile ~/.ssh/known_hosts\n")
	defer restore()

	b.Run("noReuse", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
			if err := exec.Connect(context.Background()); err != nil {
				b.Fatalf("connect: %v", err)
			}
			if _, err := exec.Run(context.Background(), "echo hi"); err != nil {
				b.Fatalf("run: %v", err)
			}
			_ = exec.Close()
		}
	})

	b.Run("reuse", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
			if err := exec.Connect(context.Background()); err != nil {
				b.Fatalf("connect: %v", err)
			}
			for j := 0; j < 30; j++ {
				if _, err := exec.Run(context.Background(), "echo hi"); err != nil {
					b.Fatalf("run: %v", err)
				}
			}
			_ = exec.Close()
		}
	})
}

// BenchmarkSSHConnectionReusePerCommand normalises the reuse benchmark
// to a per-command cost so the saving is directly comparable.
func BenchmarkSSHConnectionReusePerCommand(b *testing.B) {
	clientKey, clientSigner := generateTestKeyPair(b)
	_, hostSigner := generateTestKeyPair(b)

	server := startSSHTestServer(b, clientSigner.PublicKey(), hostSigner, map[string]sshCommandResponse{
		"echo hi": {stdout: "hi\n"},
	})
	defer server.Close()

	home := writeSSHClientEnv(b, clientKey, hostSigner, server.Host(), server.Port())
	restore := stubSSHConfigEnvWithOutput(b, home,
		"identityfile ~/.ssh/test_key\nuserknownhostsfile ~/.ssh/known_hosts\n")
	defer restore()

	const cmdsPerCollect = 30

	b.Run("noReuse", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
			if err := exec.Connect(context.Background()); err != nil {
				b.Fatalf("connect: %v", err)
			}
			if _, err := exec.Run(context.Background(), "echo hi"); err != nil {
				b.Fatalf("run: %v", err)
			}
			_ = exec.Close()
		}
	})

	b.Run("reuse_30cmd", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
			if err := exec.Connect(context.Background()); err != nil {
				b.Fatalf("connect: %v", err)
			}
			for j := 0; j < cmdsPerCollect; j++ {
				if _, err := exec.Run(context.Background(), "echo hi"); err != nil {
					b.Fatalf("run: %v", err)
				}
			}
			_ = exec.Close()
		}
		b.ReportMetric(float64(b.N*cmdsPerCollect)/b.Elapsed().Seconds(), "cmd/sec")
	})
}
