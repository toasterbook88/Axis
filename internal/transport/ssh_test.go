package transport

import "testing"

func TestParseSSHConfigDumpIdentitiesOnly(t *testing.T) {
	output := `user deploy
hostname 10.0.0.5
port 22
identityfile ~/.ssh/deploy_key
identitiesonly yes
`
	resolved := parseSSHConfigDump(output)
	if !resolved.IdentitiesOnly {
		t.Fatal("expected IdentitiesOnly true when set to yes")
	}
	if len(resolved.IdentityFiles) != 1 || resolved.IdentityFiles[0] != "~/.ssh/deploy_key" {
		t.Fatalf("expected identity file ~/.ssh/deploy_key, got %v", resolved.IdentityFiles)
	}
}

func TestParseSSHConfigDumpIdentitiesOnlyNo(t *testing.T) {
	output := `user deploy
identitiesonly no
`
	resolved := parseSSHConfigDump(output)
	if resolved.IdentitiesOnly {
		t.Fatal("expected IdentitiesOnly false when set to no")
	}
}

func TestParseSSHConfigDumpIdentitiesOnlyMissing(t *testing.T) {
	output := `user deploy
hostname 10.0.0.5
`
	resolved := parseSSHConfigDump(output)
	if resolved.IdentitiesOnly {
		t.Fatal("expected IdentitiesOnly false when not specified")
	}
}

func TestSignerPathsSkipsDefaultsWithIdentitiesOnly(t *testing.T) {
	resolved := resolvedSSHConfig{
		IdentityFiles:  []string{"/home/user/.ssh/deploy_key"},
		IdentitiesOnly: true,
	}
	paths := signerPaths("/home/user", resolved)
	for _, p := range paths {
		if p == "/home/user/.ssh/id_ed25519" || p == "/home/user/.ssh/id_rsa" || p == "/home/user/.ssh/id_ecdsa" {
			t.Fatalf("IdentitiesOnly should exclude default key %s", p)
		}
	}
	if len(paths) != 1 || paths[0] != "/home/user/.ssh/deploy_key" {
		t.Fatalf("expected only deploy_key path, got %v", paths)
	}
}

func TestSignerPathsIncludesDefaultsWithoutIdentitiesOnly(t *testing.T) {
	resolved := resolvedSSHConfig{
		IdentityFiles:  []string{"/home/user/.ssh/deploy_key"},
		IdentitiesOnly: false,
	}
	paths := signerPaths("/home/user", resolved)
	found := map[string]bool{}
	for _, p := range paths {
		found[p] = true
	}
	if !found["/home/user/.ssh/id_ed25519"] {
		t.Fatal("expected id_ed25519 default without IdentitiesOnly")
	}
	if !found["/home/user/.ssh/id_rsa"] {
		t.Fatal("expected id_rsa default without IdentitiesOnly")
	}
	if !found["/home/user/.ssh/deploy_key"] {
		t.Fatal("expected explicit identity file")
	}
}
