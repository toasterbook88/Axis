package facts

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// ollamaAPIStub answers the two local Ollama API calls the catalog makes:
// /api/tags lists two on-box models and one cloud proxy, and /api/show reports
// capabilities, so the embedding model is distinguishable from chat models.
const ollamaAPIStub = `
case "$*" in
  */api/tags*)
    echo '{"models":[
      {"name":"qwen2.5-coder:0.5b","size":397821319,"details":{"family":"qwen2","parameter_size":"494.03M","quantization_level":"Q4_K_M"}},
      {"name":"nomic-embed-text:latest","size":274302450,"details":{"family":"nomic-bert","parameter_size":"137M","quantization_level":"F16"}},
      {"name":"gpt-oss:20b-cloud","size":381,"remote_host":"https://ollama.com:443","remote_model":"gpt-oss:20b","details":{"family":"gptoss"}}
    ]}' ;;
  *nomic-embed-text*) echo '{"capabilities":["embedding"]}' ;;
  *gpt-oss*) echo '{"capabilities":["completion","tools","thinking"]}' ;;
  */api/show*) echo '{"capabilities":["completion","tools","insert"]}' ;;
  *) exit 7 ;;
esac`

// ollamaCatalogEnv layers a curl stub over the standard probe sandbox.
func ollamaCatalogEnv(t *testing.T, curlBody string) []string {
	t.Helper()
	env := ollamaProbeSandbox(t, `exit 1`)
	bin := t.TempDir()
	writeFactStub(t, bin, "curl", curlBody)
	sandboxPATH := ""
	for _, kv := range env {
		if p, ok := strings.CutPrefix(kv, "PATH="); ok {
			sandboxPATH = p // the sandbox entry is appended last
		}
	}
	return append(env, "PATH="+bin+string(os.PathListSeparator)+sandboxPATH)
}

func TestOllamaDiscoveryScriptReportsModelCatalog(t *testing.T) {
	got := runOllamaProbe(t, ollamaCatalogEnv(t, ollamaAPIStub))

	if len(got.Catalog) != 3 {
		t.Fatalf("catalog = %+v, want 3 entries", got.Catalog)
	}
	byName := map[string]int{}
	for i, e := range got.Catalog {
		byName[e.Name] = i
	}

	coder := got.Catalog[byName["qwen2.5-coder:0.5b"]]
	if !slices.Contains(coder.Capabilities, "completion") || coder.RemoteHost != "" {
		t.Fatalf("coder entry = %+v, want on-box completion model", coder)
	}
	if coder.Family != "qwen2" || coder.ParameterSize != "494.03M" || coder.Quantization != "Q4_K_M" || coder.SizeBytes != 397821319 {
		t.Fatalf("coder details = %+v, want family/parameter_size/quantization/size from /api/tags", coder)
	}

	embed := got.Catalog[byName["nomic-embed-text:latest"]]
	if !slices.Equal(embed.Capabilities, []string{"embedding"}) {
		t.Fatalf("embed capabilities = %v, want [embedding]", embed.Capabilities)
	}

	cloud := got.Catalog[byName["gpt-oss:20b-cloud"]]
	if cloud.RemoteHost != "https://ollama.com:443" || cloud.RemoteModel != "gpt-oss:20b" {
		t.Fatalf("cloud entry = %+v, want remote_host and remote_model from /api/tags", cloud)
	}
}

// When the local API is unreachable the catalog is absent, and the existing
// `ollama list` names are still reported. Missing data stays missing.
func TestOllamaDiscoveryScriptOmitsCatalogWhenAPIUnreachable(t *testing.T) {
	got := runOllamaProbe(t, ollamaCatalogEnv(t, `exit 7`))
	if len(got.Catalog) != 0 {
		t.Fatalf("catalog = %+v, want empty when /api/tags fails", got.Catalog)
	}
	if !slices.Equal(got.Models, []string{"stub:7b"}) {
		t.Fatalf("models = %v, want [stub:7b] from ollama list", got.Models)
	}
}
