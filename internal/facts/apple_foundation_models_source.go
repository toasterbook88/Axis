package facts

// appleFoundationModelsFactsSwift reads SystemLanguageModel state without
// creating a session or generating. It defines appleFMFactsJSON(). It holds
// no single quotes, so it can also travel inside a single-quoted
// xcrun swift -e argument.
const appleFoundationModelsFactsSwift = `func appleFMFactsJSON() -> String {
	let model = SystemLanguageModel.default
	var out: [String: Any] = [:]
	switch model.availability {
	case .available:
		out["availability"] = "available"
	case .unavailable(let reason):
		out["availability"] = "unavailable"
		switch reason {
		case .appleIntelligenceNotEnabled: out["reason"] = "appleIntelligenceNotEnabled"
		case .deviceNotEligible: out["reason"] = "deviceNotEligible"
		case .modelNotReady: out["reason"] = "modelNotReady"
		@unknown default: out["reason"] = "\(reason)"
		}
	}
	if model.isAvailable {
		out["context_size"] = model.contextSize
		out["languages"] = model.supportedLanguages.count
		if #available(macOS 27.0, *) {
			out["model"] = model.variant.displayName
			var caps: [String] = []
			let known: [(String, LanguageModelCapabilities.Capability)] = [("toolCalling", .toolCalling), ("guidedGeneration", .guidedGeneration), ("reasoning", .reasoning), ("vision", .vision)]
			for (name, cap) in known where model.capabilities.contains(cap) { caps.append(name) }
			out["capabilities"] = caps
		}
	}
	guard let data = try? JSONSerialization.data(withJSONObject: out, options: [.sortedKeys]) else { return "{}" }
	return String(decoding: data, as: UTF8.self)
}
`

const appleFoundationModelsHelperSource = `import Darwin
import Foundation
import FoundationModels

struct CLIError: Error, CustomStringConvertible {
	let description: String
}

` + appleFoundationModelsFactsSwift + `
func usage() -> String {
	"""
	usage: apple-foundation-models.swift --prompt <text> [--json]
	       apple-foundation-models.swift --self-test
	       apple-foundation-models.swift --facts

	Examples:
	  xcrun swift hack/apple-foundation-models.swift --prompt "Summarize this diff"
	  xcrun swift hack/apple-foundation-models.swift --self-test
	"""
}

func parseArguments() throws -> (prompt: String, json: Bool) {
	let args = Array(CommandLine.arguments.dropFirst())
	var prompt: String?
	var json = false
	var selfTest = false

	var index = 0
	while index < args.count {
		switch args[index] {
		case "--json":
			json = true
			index += 1
		case "--self-test":
			selfTest = true
			index += 1
		case "--prompt":
			guard index + 1 < args.count else {
				throw CLIError(description: "missing value for --prompt")
			}
			prompt = args[index + 1]
			index += 2
		default:
			index += 1
		}
	}

	if selfTest {
		return ("Reply with exactly OK", json)
	}

	if let prompt, !prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
		return (prompt, json)
	}

	let stdinData = FileHandle.standardInput.readDataToEndOfFile()
	if let stdinText = String(data: stdinData, encoding: .utf8)?
		.trimmingCharacters(in: .whitespacesAndNewlines),
		!stdinText.isEmpty {
		return (stdinText, json)
	}

	throw CLIError(description: usage())
}

if CommandLine.arguments.dropFirst().contains("--facts") {
	print(appleFMFactsJSON())
	Darwin.exit(0)
}

let parsed: (prompt: String, json: Bool)
do {
	parsed = try parseArguments()
} catch {
	fputs("\(error)\n", stderr)
	Darwin.exit(2)
}

do {
	let session = LanguageModelSession()
	let response = try await session.respond(to: parsed.prompt)
	if parsed.json {
		let payload: [String: Any] = [
			"ok": true,
			"content": response.content,
		]
		let data = try JSONSerialization.data(withJSONObject: payload, options: [.prettyPrinted, .sortedKeys])
		FileHandle.standardOutput.write(data)
		FileHandle.standardOutput.write(Data("\n".utf8))
	} else {
		print(response.content)
	}
} catch {
	fputs("apple foundation models error: \(error)\n", stderr)
	Darwin.exit(1)
}
`

// AppleFoundationModelsDiscoveryScript reads Apple Foundation Models facts on
// a Darwin node without generating: a cached helper's --facts mode, else the
// same read-only body through xcrun swift -e. A helper built from an older
// source has no --facts and prints no JSON, so the loop falls through; its
// stdin is /dev/null so it can never read a prompt and generate.
const AppleFoundationModelsDiscoveryScript = `set +e
for h in "$HOME/.axis/cache/apple-foundation-models-helper" "$HOME/Library/Caches/axis/apple-foundation-models-helper" "$HOME/.cache/axis/apple-foundation-models-helper"; do
  if [ -x "$h" ]; then
    out=$("$h" --facts </dev/null 2>/dev/null)
    case "$out" in "{"*) printf '%s\n' "$out"; exit 0 ;; esac
  fi
done
xcrun swift -e 'import Foundation
import FoundationModels
` + appleFoundationModelsFactsSwift + `print(appleFMFactsJSON())' 2>/dev/null && exit 0
xcrun swift -e 'import FoundationModels' 2>/dev/null && echo "UNVERIFIED" && exit 0
exit 1
`
